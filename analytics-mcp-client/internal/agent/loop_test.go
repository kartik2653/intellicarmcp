package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
)

type fakeTools struct {
	calls []string
	fail  bool
}

func (f *fakeTools) Definitions() []llm.Tool {
	return []llm.Tool{{
		Name:        "run_query",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
}

func (f *fakeTools) Execute(_ context.Context, name string, arguments json.RawMessage) (string, error) {
	f.calls = append(f.calls, name+":"+string(arguments))
	if name != "run_query" {
		return "", errors.New("unknown tool")
	}
	if f.fail {
		return "upstream failed", errors.New("MCP tool run_query returned an error")
	}
	return `[{"fleet":101,"km":10}]`, nil
}

func TestSystemTextIncludesPlanningGuidance(t *testing.T) {
	t.Parallel()
	loop := &Loop{ReferDatabase: "lafdatav1", Instructions: "Use tripreport"}
	text := loop.systemText()
	for _, phrase := range []string{"lafdatav1", "EXPLAIN ESTIMATE", "not a partial", "SQL LIMIT", "Use tripreport", "postgres_query", "Infer joins", "foreign key"} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("system text missing %q in %s", phrase, text)
		}
	}
	lower := strings.ToLower(text)
	for _, phrase := range []string{"six months", "6 months", "always summarize"} {
		if strings.Contains(lower, phrase) {
			t.Fatalf("system text contains a date rule %q", phrase)
		}
	}
}

func TestLoopFinalAnswerWithoutTools(t *testing.T) {
	t.Parallel()
	loop := testLoop(t, []string{
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"No data needed."}]}`,
	}, &fakeTools{})
	answer, err := loop.Ask(context.Background(), "hello")
	if err != nil || answer != "No data needed." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

func TestLoopOneToolCallPreservesID(t *testing.T) {
	t.Parallel()
	var requests []json.RawMessage
	server := scriptServer(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"query"},{"type":"text","text":"I'll retrieve the totals."},{"type":"tool_use","id":"call_abc","name":"run_query","input":{"query":"SELECT 1"}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"Fleet 101 travelled 10 km."}]}`,
	}, &requests)
	tools := &fakeTools{}
	loop := &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5-5", Effort: "medium", HTTPClient: server.Client()},
		Tools:        tools,
		MaxToolCalls: 10,
		Timeout:      time.Second,
	}
	var seen []string
	loop.Hooks.ToolStart = func(name string) { seen = append(seen, name) }
	answer, err := loop.Ask(context.Background(), "What is the total distance?")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Fleet 101 travelled 10 km." || len(tools.calls) != 1 || seen[0] != "run_query" {
		t.Fatalf("answer=%q calls=%v", answer, tools.calls)
	}
	raw := string(requests[1])
	if !strings.Contains(raw, `"tool_use_id":"call_abc"`) || !strings.Contains(raw, `"thinking":"query"`) {
		t.Fatalf("history dropped tool id or thinking: %s", raw)
	}
}

func TestLoopMultipleToolCallsAndLimit(t *testing.T) {
	t.Parallel()
	tools := &fakeTools{}
	loop := testLoop(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"c1","name":"run_query","input":{"query":"a"}},{"type":"tool_use","id":"c2","name":"run_query","input":{"query":"b"}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"both done"}]}`,
	}, tools)
	answer, err := loop.Ask(context.Background(), "compare")
	if err != nil || answer != "both done" || len(tools.calls) != 2 {
		t.Fatalf("answer=%q calls=%v err=%v", answer, tools.calls, err)
	}

	limited := testLoop(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"c1","name":"run_query","input":{"query":"a"}},{"type":"tool_use","id":"c2","name":"run_query","input":{"query":"b"}}]}`,
	}, &fakeTools{})
	limited.MaxToolCalls = 1
	_, err = limited.Ask(context.Background(), "compare")
	if err == nil || !strings.Contains(err.Error(), "maximum tool calls") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoopUnknownToolAndRepeatedCalls(t *testing.T) {
	t.Parallel()
	tools := &fakeTools{}
	loop := testLoop(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"c1","name":"shell","input":{}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"I cannot run that."}]}`,
	}, tools)
	answer, err := loop.Ask(context.Background(), "run a command")
	if err != nil || answer != "I cannot run that." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], "shell:") {
		t.Fatalf("calls = %v", tools.calls)
	}

	repeated := testLoop(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"c1","name":"run_query","input":{"query":"same"}}]}`,
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"c2","name":"run_query","input":{"query":"same"}}]}`,
	}, &fakeTools{})
	_, err = repeated.Ask(context.Background(), "again")
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoopKeepsFailedToolResult(t *testing.T) {
	t.Parallel()
	var rawBodies []json.RawMessage
	server := scriptServer(t, []string{
		`{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"err1","name":"run_query","input":{}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"The query failed."}]}`,
	}, &rawBodies)
	loop := &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5-5", Effort: "medium", HTTPClient: server.Client()},
		Tools:        &fakeTools{fail: true},
		MaxToolCalls: 4,
		Timeout:      time.Second,
	}
	answer, err := loop.Ask(context.Background(), "run it")
	if err != nil || answer != "The query failed." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if !strings.Contains(string(rawBodies[1]), "upstream failed") || !strings.Contains(string(rawBodies[1]), `"tool_use_id":"err1"`) {
		t.Fatalf("failed result was discarded: %s", rawBodies[1])
	}
}

func testLoop(t *testing.T, responses []string, tools Executor) *Loop {
	t.Helper()
	var ignored []json.RawMessage
	server := scriptServer(t, responses, &ignored)
	return &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5-5", Effort: "medium", HTTPClient: server.Client()},
		Tools:        tools,
		MaxToolCalls: 10,
		Timeout:      time.Second,
	}
}

func scriptServer(t *testing.T, responses []string, bodies *[]json.RawMessage) *httptest.Server {
	t.Helper()
	var n int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, append(json.RawMessage(nil), raw...))
		if n >= len(responses) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(responses[n]))
		n++
	}))
	t.Cleanup(server.Close)
	return server
}

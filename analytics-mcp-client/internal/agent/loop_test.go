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
		Type: "function",
		Function: llm.FunctionDef{
			Name:       "run_query",
			Parameters: json.RawMessage(`{"type":"object"}`),
		},
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

func TestLoopFinalAnswerWithoutTools(t *testing.T) {
	t.Parallel()
	loop := testLoop(t, []string{
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"No data needed."}}]}`,
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
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"I'll retrieve the totals.","reasoning_content":"query","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"SELECT 1\"}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Fleet 101 travelled 10 km."}}]}`,
	}, &requests)
	tools := &fakeTools{}
	loop := &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: server.Client()},
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
	if !strings.Contains(raw, `"tool_call_id":"call_abc"`) || !strings.Contains(raw, `"reasoning_content":"query"`) {
		t.Fatalf("history dropped tool id or reasoning: %s", raw)
	}
	var second llmRequest
	if err := json.Unmarshal(requests[1], &second); err != nil {
		t.Fatal(err)
	}
	if second.Messages[len(second.Messages)-1].Role != "tool" {
		t.Fatalf("last message = %+v", second.Messages[len(second.Messages)-1])
	}
}

func TestLoopMultipleToolCallsAndLimit(t *testing.T) {
	t.Parallel()
	tools := &fakeTools{}
	loop := testLoop(t, []string{
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"a\"}"}},
			{"id":"c2","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"b\"}"}}
		]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"both done"}}]}`,
	}, tools)
	answer, err := loop.Ask(context.Background(), "compare")
	if err != nil || answer != "both done" || len(tools.calls) != 2 {
		t.Fatalf("answer=%q calls=%v err=%v", answer, tools.calls, err)
	}

	limited := testLoop(t, []string{
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"a\"}"}},
			{"id":"c2","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"b\"}"}}
		]}}]}`,
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
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"I cannot run that."}}]}`,
	}, tools)
	answer, err := loop.Ask(context.Background(), "run a command")
	if err != nil || answer != "I cannot run that." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], "shell:") {
		t.Fatalf("calls = %v", tools.calls)
	}

	repeated := testLoop(t, []string{
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"same\"}"}}]}}]}`,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c2","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"same\"}"}}]}}]}`,
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
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"err1","type":"function","function":{"name":"run_query","arguments":"{}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"The query failed."}}]}`,
	}, &rawBodies)
	loop := &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: server.Client()},
		Tools:        &fakeTools{fail: true},
		MaxToolCalls: 4,
		Timeout:      time.Second,
	}
	answer, err := loop.Ask(context.Background(), "run it")
	if err != nil || answer != "The query failed." {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if !strings.Contains(string(rawBodies[1]), "upstream failed") || !strings.Contains(string(rawBodies[1]), `"tool_call_id":"err1"`) {
		t.Fatalf("failed result was discarded: %s", rawBodies[1])
	}
}

type llmRequest struct {
	Messages []llm.Message `json:"messages"`
}

func testLoop(t *testing.T, responses []string, tools Executor) *Loop {
	t.Helper()
	var ignored []json.RawMessage
	server := scriptServer(t, responses, &ignored)
	return &Loop{
		LLM:          &llm.Client{BaseURL: server.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: server.Client()},
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

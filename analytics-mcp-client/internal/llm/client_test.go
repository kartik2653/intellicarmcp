package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteParsesToolCalls(t *testing.T) {
	t.Parallel()
	var got messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("api key header = %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("version = %s", r.Header.Get("anthropic-version"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason":"tool_use",
			"content":[
				{"type":"thinking","thinking":"need a query"},
				{"type":"text","text":"I'll look that up."},
				{"type":"tool_use","id":"call_1","name":"run_query","input":{"query":"SELECT 1"}}
			]
		}`))
	}))
	t.Cleanup(server.Close)

	client := &Client{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-5-5", Effort: "medium", HTTPClient: server.Client()}
	user, _ := json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: "How many fleets?"})
	completion, err := client.Complete(context.Background(), "Use the tools.", []json.RawMessage{user}, []Tool{{
		Name:        "run_query",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-sonnet-5-5" || got.ToolChoice == nil || got.ToolChoice.Type != "auto" || got.OutputConfig.Effort != "medium" || len(got.Tools) != 1 {
		t.Fatalf("request = %+v", got)
	}
	if strings.Contains(string(mustJSON(got)), "temperature") {
		t.Fatal("request included a sampling parameter")
	}
	if completion.ToolCalls[0].ID != "call_1" || completion.Text != "I'll look that up." || !strings.Contains(string(completion.RawMessage), "need a query") {
		t.Fatalf("completion = %+v raw=%s", completion, completion.RawMessage)
	}
}

func TestCompleteNoToolCall(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"Fleet 101 travelled 10 km."}]}`))
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5-5", HTTPClient: server.Client()}
	completion, err := client.Complete(context.Background(), "", []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Text != "Fleet 101 travelled 10 km." || len(completion.ToolCalls) != 0 {
		t.Fatalf("message = %+v", completion)
	}
}

func TestCompleteAPIErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{"error":{"message":"bad key test-key"}}`, nil},
		{http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, ErrRateLimited},
		{http.StatusNotFound, `{"error":{"message":"model claude-missing does not exist"}}`, ErrModelUnavailable},
		{http.StatusBadRequest, `{"error":{"message":"maximum context length exceeded"}}`, ErrContextLimit},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := &Client{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-5-5", HTTPClient: server.Client()}
		_, err := client.Complete(context.Background(), "", []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
		server.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", tc.status)
		}
		if strings.Contains(err.Error(), "test-key") {
			t.Fatalf("error leaked the API key: %s", err.Error())
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("status %d err = %v", tc.status, err)
		}
	}
}

func TestCompleteMalformedAndTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, APIKey: "k", Model: "claude-sonnet-5-5", HTTPClient: server.Client()}
	_, err := client.Complete(context.Background(), "", []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slowClient := &Client{BaseURL: slow.URL, APIKey: "k", Model: "claude-sonnet-5-5", HTTPClient: slow.Client()}
	_, err = slowClient.Complete(ctx, "", []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

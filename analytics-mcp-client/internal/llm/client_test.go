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
	var got chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"index":0,"finish_reason":"tool_calls","message":{
				"role":"assistant",
				"content":"I'll look that up.",
				"reasoning_content":"need a query",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_query","arguments":"{\"query\":\"SELECT 1\"}"}}]
			}}]
		}`))
	}))
	t.Cleanup(server.Close)

	client := &Client{BaseURL: server.URL, APIKey: "test-key", Model: "grok-4.6", HTTPClient: server.Client()}
	user, _ := json.Marshal(Message{Role: "user", Content: "How many fleets?"})
	completion, err := client.Complete(context.Background(), []json.RawMessage{user}, []Tool{{
		Type:     "function",
		Function: FunctionDef{Name: "run_query", Parameters: json.RawMessage(`{"type":"object"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "grok-4.6" || got.ToolChoice != "auto" || len(got.Tools) != 1 {
		t.Fatalf("request = %+v", got)
	}
	if completion.Message.ToolCalls[0].ID != "call_1" || !strings.Contains(string(completion.RawMessage), "reasoning_content") {
		t.Fatalf("completion = %+v raw=%s", completion.Message, completion.RawMessage)
	}
}

func TestCompleteNoToolCall(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Fleet 101 travelled 10 km."}}]}`))
	}))
	t.Cleanup(server.Close)
	client := &Client{BaseURL: server.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: server.Client()}
	completion, err := client.Complete(context.Background(), []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Message.Content != "Fleet 101 travelled 10 km." || len(completion.Message.ToolCalls) != 0 {
		t.Fatalf("message = %+v", completion.Message)
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
		{http.StatusNotFound, `{"error":{"message":"model grok-missing does not exist"}}`, ErrModelUnavailable},
		{http.StatusBadRequest, `{"error":{"message":"maximum context length exceeded"}}`, ErrContextLimit},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		client := &Client{BaseURL: server.URL, APIKey: "test-key", Model: "grok-4.6", HTTPClient: server.Client()}
		_, err := client.Complete(context.Background(), []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
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
	client := &Client{BaseURL: server.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: server.Client()}
	_, err := client.Complete(context.Background(), []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slowClient := &Client{BaseURL: slow.URL, APIKey: "k", Model: "grok-4.6", HTTPClient: slow.Client()}
	_, err = slowClient.Complete(ctx, []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
}

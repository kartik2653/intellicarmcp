package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	apiVersion     = "2023-06-01"
	defaultEffort  = "medium"
	defaultMaxToks = 32000
)

// Client calls the Anthropic Messages API.
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	Effort     string
	MaxTokens  int
	HTTPClient *http.Client
}

// APIError is a non-2xx response from Anthropic.
type APIError struct {
	Status  int
	Message string
	Kind    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Anthropic API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("Anthropic API returned HTTP %d: %s", e.Status, e.Message)
}

// ErrModelUnavailable means the requested model was rejected.
var ErrModelUnavailable = fmt.Errorf("Anthropic model is unavailable for this account")

// ErrRateLimited means the API asked the client to slow down.
var ErrRateLimited = fmt.Errorf("Anthropic API rate limit reached")

// ErrContextLimit means the request or completion exceeded a token limit.
var ErrContextLimit = fmt.Errorf("Anthropic request exceeded the context or token limit")

type messagesRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       string            `json:"system,omitempty"`
	Messages     []json.RawMessage `json:"messages"`
	Tools        []Tool            `json:"tools,omitempty"`
	ToolChoice   *toolChoice       `json:"tool_choice,omitempty"`
	OutputConfig outputConfig      `json:"output_config"`
}

type toolChoice struct {
	Type string `json:"type"`
}

type outputConfig struct {
	Effort string `json:"effort"`
}

// Complete posts one Messages request. The model name is sent unchanged.
func (c *Client) Complete(ctx context.Context, system string, messages []json.RawMessage, tools []Tool) (Completion, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	effort := strings.TrimSpace(c.Effort)
	if effort == "" {
		effort = defaultEffort
	}
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxToks
	}
	payload := messagesRequest{
		Model:        c.Model,
		MaxTokens:    maxTokens,
		System:       system,
		Messages:     messages,
		Tools:        tools,
		OutputConfig: outputConfig{Effort: effort},
	}
	if len(tools) > 0 {
		payload.ToolChoice = &toolChoice{Type: "auto"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ctx.Err()
		}
		return Completion{}, fmt.Errorf("Anthropic request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Completion{}, fmt.Errorf("read Anthropic response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Completion{}, c.httpError(resp.StatusCode, raw)
	}
	var decoded struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Completion{}, fmt.Errorf("Anthropic response was not valid JSON")
	}
	if len(decoded.Content) == 0 || string(decoded.Content) == "null" {
		return Completion{}, fmt.Errorf("Anthropic response did not include assistant content")
	}
	text, calls, err := parseContent(decoded.Content)
	if err != nil {
		return Completion{}, err
	}
	assistant, err := json.Marshal(struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}{Role: "assistant", Content: decoded.Content})
	if err != nil {
		return Completion{}, err
	}
	completion := Completion{
		RawMessage:   assistant,
		Text:         text,
		ToolCalls:    calls,
		FinishReason: decoded.StopReason,
	}
	if decoded.StopReason == "max_tokens" {
		return completion, ErrContextLimit
	}
	return completion, nil
}

func parseContent(raw json.RawMessage) (string, []ToolCall, error) {
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, fmt.Errorf("Anthropic assistant content was not valid JSON")
	}
	var text strings.Builder
	var calls []ToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text.Len() > 0 && block.Text != "" {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
		case "tool_use":
			input := block.Input
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Input: append(json.RawMessage(nil), input...)})
		}
	}
	return text.String(), calls, nil
}

func (c *Client) httpError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	var decoded struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.Error.Message != "" {
		message = decoded.Error.Message
	}
	message = redact(message, c.APIKey)
	err := &APIError{Status: status, Message: message, Kind: "api"}
	switch {
	case status == http.StatusTooManyRequests:
		err.Kind = "rate_limit"
		return fmt.Errorf("%w: %s", ErrRateLimited, err.Error())
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		err.Kind = "auth"
		return err
	case isModelError(status, message):
		err.Kind = "model"
		return fmt.Errorf("%w: %s", ErrModelUnavailable, message)
	case isContextError(message):
		err.Kind = "context"
		return fmt.Errorf("%w: %s", ErrContextLimit, message)
	default:
		return err
	}
}

func isModelError(status int, message string) bool {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "model") {
		return false
	}
	return status == http.StatusNotFound || strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "invalid") || strings.Contains(lower, "unavailable")
}

func isContextError(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "context length") || strings.Contains(lower, "maximum context") || strings.Contains(lower, "too many tokens") || strings.Contains(lower, "token limit")
}

func redact(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[redacted]")
}

// TimeoutClient returns an HTTP client whose requests stop at timeout.
func TimeoutClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

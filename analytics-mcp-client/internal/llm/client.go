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

const toolChoiceAuto = "auto"

// Client calls the xAI Chat Completions API.
type Client struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// APIError is a non-2xx response from xAI.
type APIError struct {
	Status  int
	Message string
	Kind    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("xAI API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("xAI API returned HTTP %d: %s", e.Status, e.Message)
}

// ErrModelUnavailable means the requested model was rejected.
var ErrModelUnavailable = fmt.Errorf("xAI model is unavailable for this account")

// ErrRateLimited means the API asked the client to slow down.
var ErrRateLimited = fmt.Errorf("xAI API rate limit reached")

// ErrContextLimit means the request or completion exceeded a token limit.
var ErrContextLimit = fmt.Errorf("xAI request exceeded the context or token limit")

type chatRequest struct {
	Model      string            `json:"model"`
	Messages   []json.RawMessage `json:"messages"`
	Tools      []Tool            `json:"tools,omitempty"`
	ToolChoice string            `json:"tool_choice,omitempty"`
}

// Complete posts one chat completion. The model name is sent unchanged.
func (c *Client) Complete(ctx context.Context, messages []json.RawMessage, tools []Tool) (Completion, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	payload := chatRequest{
		Model:      c.Model,
		Messages:   messages,
		Tools:      tools,
		ToolChoice: toolChoiceAuto,
	}
	if len(tools) == 0 {
		payload.ToolChoice = ""
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Completion{}, ctx.Err()
		}
		return Completion{}, fmt.Errorf("xAI request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Completion{}, fmt.Errorf("read xAI response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Completion{}, c.httpError(resp.StatusCode, raw)
	}
	var decoded struct {
		Choices []struct {
			Message      json.RawMessage `json:"message"`
			FinishReason string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Completion{}, fmt.Errorf("xAI response was not valid JSON")
	}
	if len(decoded.Choices) == 0 || len(decoded.Choices[0].Message) == 0 {
		return Completion{}, fmt.Errorf("xAI response did not include an assistant message")
	}
	var message Message
	if err := json.Unmarshal(decoded.Choices[0].Message, &message); err != nil {
		return Completion{}, fmt.Errorf("xAI assistant message was not valid JSON")
	}
	if decoded.Choices[0].FinishReason == "length" {
		return Completion{
			RawMessage:   decoded.Choices[0].Message,
			Message:      message,
			FinishReason: decoded.Choices[0].FinishReason,
		}, ErrContextLimit
	}
	return Completion{
		RawMessage:   decoded.Choices[0].Message,
		Message:      message,
		FinishReason: decoded.Choices[0].FinishReason,
	}, nil
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
	kind := "api"
	err := &APIError{Status: status, Message: message, Kind: kind}
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

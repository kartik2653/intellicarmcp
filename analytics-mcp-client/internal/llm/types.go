package llm

import "encoding/json"

// Tool is an Anthropic Messages tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolCall is one tool_use block requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Completion is one non-streaming Messages response.
type Completion struct {
	RawMessage   json.RawMessage
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
}

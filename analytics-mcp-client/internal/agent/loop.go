package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
)

const systemPrompt = "You answer analytics questions using the provided MCP tools. Base factual answers on tool results. Do not invent rows, metrics, or tool results."

// Executor runs a tool that was discovered from MCP.
type Executor interface {
	Definitions() []llm.Tool
	Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

// Hooks report tool activity to the terminal.
type Hooks struct {
	Assistant func(text string)
	ToolStart func(name string)
	ToolDone  func(name string, elapsed time.Duration, err error)
}

// Loop is the Claude tool-calling conversation.
type Loop struct {
	LLM           *llm.Client
	Tools         Executor
	MaxToolCalls  int
	Timeout       time.Duration
	ReferDatabase string
	Instructions  string
	Hooks         Hooks
	history       []json.RawMessage
}

// Ask sends one plain-text user question and returns the final assistant answer.
func (l *Loop) Ask(ctx context.Context, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("question is empty")
	}
	if l.MaxToolCalls <= 0 {
		return "", fmt.Errorf("MAX_TOOL_CALLS must be positive")
	}
	if l.history == nil {
		l.history = []json.RawMessage{}
	}
	user, err := marshalMessage(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: question})
	if err != nil {
		return "", err
	}
	l.history = append(l.history, user)

	askCtx := ctx
	cancel := func() {}
	if l.Timeout > 0 {
		askCtx, cancel = context.WithTimeout(ctx, l.Timeout)
	}
	defer cancel()

	used := 0
	var previousSignature string
	for {
		if err := askCtx.Err(); err != nil {
			return "", err
		}
		completion, err := l.LLM.Complete(askCtx, l.systemText(), l.history, l.Tools.Definitions())
		if err != nil && !errors.Is(err, llm.ErrContextLimit) {
			return "", err
		}
		if errors.Is(err, llm.ErrContextLimit) && len(completion.ToolCalls) == 0 {
			if completion.Text != "" {
				return completion.Text, err
			}
			return "", err
		}
		if len(completion.ToolCalls) == 0 {
			l.history = append(l.history, completion.RawMessage)
			return completion.Text, nil
		}
		if strings.TrimSpace(completion.Text) != "" && l.Hooks.Assistant != nil {
			l.Hooks.Assistant(completion.Text)
		}
		l.history = append(l.history, completion.RawMessage)
		signature := callSignature(completion.ToolCalls)
		if signature != "" && signature == previousSignature {
			return "", fmt.Errorf("stopped repeated tool calls")
		}
		previousSignature = signature
		blocks := make([]toolResult, 0, len(completion.ToolCalls))
		for _, call := range completion.ToolCalls {
			if used >= l.MaxToolCalls {
				return "", fmt.Errorf("maximum tool calls (%d) reached", l.MaxToolCalls)
			}
			used++
			blocks = append(blocks, toolResult{
				Type:      "tool_result",
				ToolUseID: call.ID,
				Content:   l.execute(askCtx, call),
			})
		}
		result, err := marshalMessage(struct {
			Role    string       `json:"role"`
			Content []toolResult `json:"content"`
		}{Role: "user", Content: blocks})
		if err != nil {
			return "", err
		}
		l.history = append(l.history, result)
	}
}

type toolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

func (l *Loop) systemText() string {
	text := systemPrompt
	if l.ReferDatabase != "" {
		text += " Only reference the ClickHouse database " + l.ReferDatabase + ". Do not use any other database."
	}
	if strings.TrimSpace(l.Instructions) != "" {
		text += "\n" + strings.TrimSpace(l.Instructions)
	}
	return text
}

func (l *Loop) execute(ctx context.Context, call llm.ToolCall) string {
	if l.Hooks.ToolStart != nil {
		l.Hooks.ToolStart(call.Name)
	}
	start := time.Now()
	text, err := l.Tools.Execute(ctx, call.Name, call.Input)
	elapsed := time.Since(start)
	if l.Hooks.ToolDone != nil {
		l.Hooks.ToolDone(call.Name, elapsed, err)
	}
	if err != nil {
		if text == "" {
			return err.Error()
		}
		return text + "\n" + err.Error()
	}
	return text
}

func marshalMessage(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func callSignature(calls []llm.ToolCall) string {
	var b strings.Builder
	for _, call := range calls {
		b.WriteString(call.Name)
		b.WriteByte('\n')
		b.Write(call.Input)
		b.WriteByte('\n')
	}
	return b.String()
}

package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

type scriptAsker struct {
	got []string
}

func (s *scriptAsker) Ask(_ context.Context, question string) (string, error) {
	s.got = append(s.got, question)
	return "Fleet 101 travelled 10 km.", nil
}

func TestInteractiveTextInput(t *testing.T) {
	asker := &scriptAsker{}
	var out strings.Builder
	app := &App{
		In:        strings.NewReader("\nWhat is the total distance travelled by each fleet last month?\n/reload\nexit\n"),
		Out:       &out,
		Model:     "claude-sonnet-5-5",
		ToolNames: []string{"list_databases", "list_tables", "run_query"},
		Handshake: Handshake{
			Endpoint:      "http://127.0.0.1:8080/mcp",
			Protocol:      "2025-11-25",
			ServerName:    "data-mcp-gateway",
			ServerVersion: "0.1.0",
			Instructions:  "Use tripreport",
			Tools: []ToolInfo{{
				Name:        "run_query",
				Description: "Execute SQL against the catalog",
				InputSchema: `{"type":"object"}`,
			}},
		},
		Asker: asker,
		Reload: func(context.Context) (Handshake, []string, error) {
			return Handshake{
				Endpoint: "http://127.0.0.1:8080/mcp",
				Protocol: "2025-11-25",
				Tools: []ToolInfo{{
					Name:        "get_schema_catalog",
					Description: "Static schema catalog",
					InputSchema: `{"type":"object"}`,
				}},
			}, []string{"get_schema_catalog"}, nil
		},
	}
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asker.got) != 1 || !strings.Contains(asker.got[0], "total distance") {
		t.Fatalf("questions = %#v", asker.got)
	}
	text := out.String()
	if !strings.Contains(text, "Model: claude-sonnet-5-5") || !strings.Contains(text, "Tools discovered: 3") || !strings.Contains(text, "run_query") {
		t.Fatalf("banner = %s", text)
	}
	if !strings.Contains(text, "Execute SQL against the catalog") || !strings.Contains(text, "Static schema catalog") || strings.Contains(text, "user-token") || strings.Contains(text, "Authorization") {
		t.Fatalf("handshake = %s", text)
	}
	if !strings.Contains(text, "Assistant: Fleet 101 travelled 10 km.") {
		t.Fatalf("output = %s", text)
	}
	app.PrintToolStart("run_query")
	app.Verbose = true
	app.PrintToolDone("run_query", 15*time.Millisecond, nil)
	if !strings.Contains(out.String(), "[Calling tool: run_query]") || !strings.Contains(out.String(), "finished in") {
		t.Fatalf("tool lines = %s", out.String())
	}
}

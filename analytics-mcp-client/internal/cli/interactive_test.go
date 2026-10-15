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
		In:        strings.NewReader("\nWhat is the total distance travelled by each fleet last month?\nexit\n"),
		Out:       &out,
		Model:     "grok-4.6",
		ToolNames: []string{"list_databases", "list_tables", "run_query"},
		Asker:     asker,
	}
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asker.got) != 1 || !strings.Contains(asker.got[0], "total distance") {
		t.Fatalf("questions = %#v", asker.got)
	}
	text := out.String()
	if !strings.Contains(text, "Model: grok-4.6") || !strings.Contains(text, "Tools discovered: 3") || !strings.Contains(text, "run_query") {
		t.Fatalf("banner = %s", text)
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

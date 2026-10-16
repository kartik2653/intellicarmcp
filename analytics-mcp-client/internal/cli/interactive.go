package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Asker answers one terminal question.
type Asker interface {
	Ask(ctx context.Context, question string) (string, error)
}

// ToolInfo is one tool description printed during the handshake trace.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema string
}

// Handshake is the MCP initialize result and tool list.
type Handshake struct {
	Endpoint      string
	Protocol      string
	ServerName    string
	ServerVersion string
	Instructions  string
	Tools         []ToolInfo
}

// App is the interactive terminal session.
type App struct {
	In        io.Reader
	Out       io.Writer
	Model     string
	ToolNames []string
	Handshake Handshake
	Verbose   bool
	Asker     Asker
	Reload    func(ctx context.Context) (Handshake, []string, error)
}

// Run prints the banner and reads questions until exit, quit, or EOF.
func (a *App) Run(ctx context.Context) error {
	if a.Asker == nil {
		return fmt.Errorf("client is not ready")
	}
	PrintHandshake(a.Out, a.Handshake)
	a.printBanner()
	scanner := bufio.NewScanner(a.In)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for {
		fmt.Fprint(a.Out, "You: ")
		if !scanner.Scan() {
			fmt.Fprintln(a.Out)
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch strings.ToLower(line) {
		case "exit", "quit":
			return nil
		case "/reload":
			if a.Reload == nil {
				fmt.Fprintln(a.Out, "Tool reload is not available.")
				continue
			}
			handshake, names, err := a.Reload(ctx)
			if err != nil {
				fmt.Fprintf(a.Out, "Error: %s\n", err.Error())
				continue
			}
			a.Handshake = handshake
			a.ToolNames = names
			PrintHandshake(a.Out, handshake)
			fmt.Fprintf(a.Out, "Tools discovered: %d\n", len(names))
			continue
		}
		answer, err := a.Asker.Ask(ctx, line)
		if err != nil && answer == "" {
			fmt.Fprintf(a.Out, "Error: %s\n", err.Error())
			continue
		}
		if answer != "" {
			fmt.Fprintf(a.Out, "Assistant: %s\n", answer)
		}
		if err != nil {
			fmt.Fprintf(a.Out, "Error: %s\n", err.Error())
		}
	}
}

// PrintHandshake writes the initialize result and each tool description.
// It does not print credentials.
func PrintHandshake(w io.Writer, handshake Handshake) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, "MCP handshake")
	fmt.Fprintf(w, "Endpoint: %s\n", handshake.Endpoint)
	fmt.Fprintf(w, "Protocol: %s\n", handshake.Protocol)
	fmt.Fprintf(w, "Server: %s %s\n", handshake.ServerName, handshake.ServerVersion)
	if strings.TrimSpace(handshake.Instructions) != "" {
		fmt.Fprintf(w, "Instructions:\n%s\n", handshake.Instructions)
	}
	for _, tool := range handshake.Tools {
		fmt.Fprintf(w, "Tool: %s\n", tool.Name)
		fmt.Fprintf(w, "Description: %s\n", tool.Description)
		fmt.Fprintf(w, "Input schema: %s\n", tool.InputSchema)
	}
	fmt.Fprintln(w)
}

func (a *App) printBanner() {
	fmt.Fprintln(a.Out, "Analytics MCP Client")
	fmt.Fprintf(a.Out, "Model: %s\n", a.Model)
	fmt.Fprintln(a.Out, "MCP: connected")
	fmt.Fprintf(a.Out, "Tools discovered: %d\n", len(a.ToolNames))
	if len(a.ToolNames) > 0 {
		fmt.Fprintf(a.Out, "Tools: %s\n", strings.Join(a.ToolNames, ", "))
	}
	fmt.Fprintln(a.Out)
}

// PrintToolStart formats a tool invocation line.
func (a *App) PrintToolStart(name string) {
	fmt.Fprintf(a.Out, "[Calling tool: %s]\n", name)
}

// PrintToolDone formats a verbose duration line.
func (a *App) PrintToolDone(name string, elapsed time.Duration, err error) {
	if !a.Verbose {
		return
	}
	if err != nil {
		fmt.Fprintf(a.Out, "[Tool %s failed in %s]\n", name, elapsed.Round(time.Millisecond))
		return
	}
	fmt.Fprintf(a.Out, "[Tool %s finished in %s]\n", name, elapsed.Round(time.Millisecond))
}

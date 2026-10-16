package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const publicPath = "/mcp"

// Server is the public MCP endpoint.
type Server struct {
	logger     *slog.Logger
	sources    []Source
	mcp        http.Handler
	httpServer *http.Server
	queryWait  time.Duration
}

// New builds a gateway server. queryWait should be the configured query timeout.
func New(logger *slog.Logger, sources []Source, queryWait time.Duration) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if queryWait <= 0 {
		queryWait = 30 * time.Second
	}
	return &Server{logger: logger, sources: sources, queryWait: queryWait}
}

// Handler returns the public HTTP handler after Bind.
func (s *Server) Handler() http.Handler {
	return s.mcp
}

// Bind connects each source, registers its tools, and prepares the HTTP handler.
func (s *Server) Bind(ctx context.Context) error {
	registry := NewRegistry()
	for _, source := range s.sources {
		s.logger.Info("upstream_connecting", "source", source.Name())
		if err := source.Connect(ctx); err != nil {
			s.closeSources()
			return fmt.Errorf("connect %s: %w", source.Name(), err)
		}
		regs, err := source.Registrations(ctx)
		if err != nil {
			s.closeSources()
			return fmt.Errorf("register %s: %w", source.Name(), err)
		}
		names := make([]string, 0, len(regs))
		for _, reg := range regs {
			if err := registry.Register(reg); err != nil {
				s.closeSources()
				return err
			}
			names = append(names, reg.Tool.Name)
		}
		s.logger.Info("tools_discovered", "source", source.Name(), "tools", names)
	}

	var instructions []string
	for _, source := range s.sources {
		if instructor, ok := source.(Instructor); ok {
			if text := strings.TrimSpace(instructor.Instructions()); text != "" {
				instructions = append(instructions, text)
			}
		}
	}
	mcpServer := newMCPServer(strings.Join(instructions, "\n\n"), s.logger)
	registry.Mount(mcpServer)
	stream := newStreamHandler(mcpServer)
	mux := http.NewServeMux()
	mux.Handle(publicPath, stream)
	s.mcp = mux
	return nil
}

// Run binds the sources, serves Streamable HTTP, and shuts down when ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	if err := s.Bind(ctx); err != nil {
		return err
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.mcp,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       s.queryWait + 15*time.Second,
		WriteTimeout:      s.queryWait + 15*time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("gateway_listening", "addr", addr, "path", publicPath)
		err := s.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		s.shutdown()
		return err
	case <-ctx.Done():
		s.logger.Info("gateway_shutting_down")
		s.shutdown()
		return nil
	}
}

// Close closes upstream sources. It is safe to call more than once.
func (s *Server) Close() {
	s.closeSources()
}

func (s *Server) shutdown() {
	if s.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("http_shutdown", "error", err.Error())
		}
	}
	s.closeSources()
	s.logger.Info("gateway_stopped")
}

func (s *Server) closeSources() {
	for _, source := range s.sources {
		if err := source.Close(); err != nil {
			s.logger.Error("upstream_close", "source", source.Name(), "error", err.Error())
		}
	}
}

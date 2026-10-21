package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/authz"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/config"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/gateway"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/observability"
	"github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/policy"
	pgpolicy "github.com/Intellicars/intellicarmcp/data-mcp-gateway/internal/policy/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Service is the PostgreSQL source module.
type Service struct {
	cfg      config.Postgres
	limits   policy.Limits
	policy   *pgpolicy.QueryPolicy
	database string
	logSQL   bool
	logger   *slog.Logger
	perms    authz.Fetcher
	traceSeq atomic.Uint64
	timeout  time.Duration
	client   *Client
	meta     *MetadataCache
}

// SetPermissions attaches the user-scope fetcher used before postgres_query.
func (s *Service) SetPermissions(fetcher authz.Fetcher) {
	if s != nil {
		s.perms = fetcher
	}
}

// New returns a PostgreSQL source that is not yet connected.
func New(cfg config.Postgres, logSQL bool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		cfg:      cfg,
		logSQL:   logSQL,
		logger:   logger,
		database: cfg.Database,
		limits: policy.Limits{
			MaxResultRows:    cfg.MaxResultRows,
			MaxResponseBytes: cfg.MaxResponseBytes,
		},
		policy:  pgpolicy.New(pgpolicy.Options{AllowedTables: AllowedTableColumns()}),
		timeout: cfg.QueryTimeout,
		meta:    newMetadataCache(),
	}
}

// Name implements gateway.Source.
func (s *Service) Name() string { return "postgres" }

// Connect implements gateway.Source.
func (s *Service) Connect(ctx context.Context) error {
	client, err := Dial(ctx, s.cfg, s.logger)
	if err != nil {
		return err
	}
	s.client = client
	if err := s.refreshMetadata(ctx); err != nil {
		// Allowlist remains available; catalog will note metadata_hydrated=false.
		s.logger.Warn("postgres_metadata_hydrate_failed", "error", err.Error())
	}
	return nil
}

// Registrations implements gateway.Source.
func (s *Service) Registrations(context.Context) ([]gateway.Registration, error) {
	if s.client == nil {
		return nil, fmt.Errorf("postgres source is not connected")
	}
	if _, ok := s.client.Tool(upstreamQueryTool); !ok {
		return nil, ErrQueryToolMissing
	}
	if _, ok := s.client.Tool(upstreamModifyTool); ok {
		s.logger.Info("postgres_modify_tool_hidden", "tool", upstreamModifyTool)
	}
	return []gateway.Registration{
		{Tool: publicPostgresQueryTool(s.database), Handler: s.handleQuery},
		{Tool: schemaCatalogTool(s.database), Handler: s.handleSchema},
	}, nil
}

// Instructions implements gateway.Instructor.
func (s *Service) Instructions() string {
	return BriefInstructions(s.database) + "\n\n" + LimitInstructions(s.limits.MaxResultRows, s.limits.MaxResponseBytes, s.timeout)
}

func (s *Service) handleSchema(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.meta != nil && s.meta.FetchedAt().IsZero() && s.client != nil {
		if err := s.refreshMetadata(ctx); err != nil {
			s.logger.Warn("postgres_metadata_refresh_failed", "error", err.Error())
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s.catalogJSON()}}}, nil
}

func (s *Service) catalogJSON() string {
	var meta map[string]TableMeta
	var fetched time.Time
	if s.meta != nil {
		meta = s.meta.Snapshot()
		fetched = s.meta.FetchedAt()
	}
	return CatalogJSON(s.database, meta, fetched)
}

func (s *Service) refreshMetadata(ctx context.Context) error {
	if s.client == nil || s.meta == nil {
		return fmt.Errorf("postgres source is not connected")
	}
	tables, err := hydrateMetadata(ctx, s.client, QueryTables())
	if err != nil {
		return err
	}
	s.meta.Replace(tables)
	s.logger.Info("postgres_metadata_hydrated", "tables", len(tables), "fetched_at", s.meta.FetchedAt().Format(time.RFC3339))
	return nil
}

// Close implements gateway.Source.
func (s *Service) Close() error {
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
}

func (s *Service) handleQuery(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := decodeArgs(req)
	if err != nil {
		s.logger.Warn("postgres_invalid_arguments", "error", err.Error())
		return toolError(err.Error()), nil
	}
	query, _ := args["query"].(string)
	traceID := s.nextTrace()
	if query == "" {
		s.trace(traceID, "rejected", "reason", "query is required")
		return toolError("query is required and must be a string"), nil
	}
	s.trace(traceID, "received", "database", s.database, "sql", s.sqlForLog(query))
	if err := s.policy.Validate(query); err != nil {
		s.trace(traceID, "rejected", "database", s.database, "sql", s.sqlForLog(query), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	scoped, err := s.authorizeQuery(ctx, req, query)
	if err != nil {
		s.trace(traceID, "rejected", "database", s.database, "sql", s.sqlForLog(query), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	forward := s.client.QueryArgs(scoped)
	s.trace(traceID, "accepted", "database", s.database, "sql", s.sqlForLog(scoped))
	return s.invoke(ctx, traceID, forward, scoped)
}

func (s *Service) authorizeQuery(ctx context.Context, req *mcp.CallToolRequest, query string) (string, error) {
	if s.perms == nil {
		return "", fmt.Errorf("authorization is not configured")
	}
	var header http.Header
	if req != nil && req.Extra != nil {
		header = req.Extra.Header
	}
	token, err := authz.BearerToken(header)
	if err != nil {
		return "", err
	}
	scope, err := s.perms.Fetch(ctx, token)
	if err != nil {
		return "", err
	}
	return pgpolicy.ApplyScope(query, bindingsFor(scope), AllowedTableColumns())
}

func (s *Service) invoke(ctx context.Context, traceID string, args map[string]any, sql string) (*mcp.CallToolResult, error) {
	s.trace(traceID, "upstream", "database", s.database, "sql", s.sqlForLog(sql))
	start := time.Now()
	result, err := s.client.Call(ctx, upstreamQueryTool, args)
	elapsed := time.Since(start)
	if err != nil {
		s.trace(traceID, "upstream_error", "database", s.database, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "error", err.Error())
		if errors.Is(err, context.DeadlineExceeded) {
			return toolError("query timed out. The result was not returned and is not a partial report."), nil
		}
		if errors.Is(err, context.Canceled) {
			return toolError("query cancelled"), nil
		}
		return toolError("upstream tool call failed"), nil
	}
	limited, err := policy.EnforceResult(result, s.limits)
	if err != nil {
		s.trace(traceID, "result_rejected", "database", s.database, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	attrs := []any{"database", s.database, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "is_error", limited.IsError}
	if limited.IsError {
		if detail := toolResultText(limited); detail != "" {
			attrs = append(attrs, "error", detail)
		}
		s.trace(traceID, "upstream_error", attrs...)
		return limited, nil
	}
	s.trace(traceID, "completed", attrs...)
	return limited, nil
}

func (s *Service) nextTrace() string {
	return fmt.Sprintf("pg%d", s.traceSeq.Add(1))
}

func (s *Service) sqlForLog(sql string) string {
	if sql == "" {
		return ""
	}
	if s.logSQL {
		return sql
	}
	return observability.RedactSQL(sql)
}

func (s *Service) trace(id, stage string, attrs ...any) {
	fields := make([]any, 0, 4+len(attrs))
	if id != "" {
		fields = append(fields, "trace", id)
	}
	fields = append(fields, "stage", stage)
	fields = append(fields, attrs...)
	if stage == "rejected" || stage == "upstream_error" || stage == "result_rejected" {
		s.logger.Warn("postgres_query", fields...)
		return
	}
	s.logger.Info("postgres_query", fields...)
}

func decodeArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
	}
}

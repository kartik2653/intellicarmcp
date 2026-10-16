package clickhouse

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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Service is the ClickHouse source module.
type Service struct {
	cfg           config.ClickHouse
	limits        policy.Limits
	policy        policy.QueryPolicy
	referDatabase string
	logSQL        bool
	logger        *slog.Logger
	perms         authz.Fetcher
	traceSeq      atomic.Uint64
	timeout       time.Duration
	client        *Client
}

// SetPermissions attaches the user-scope fetcher used before run_query.
func (s *Service) SetPermissions(fetcher authz.Fetcher) {
	if s != nil {
		s.perms = fetcher
	}
}

// New returns a ClickHouse source that is not yet connected.
func New(cfg config.ClickHouse, pol config.Policy, logSQL bool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		cfg:    cfg,
		logSQL: logSQL,
		logger: logger,
		limits: policy.Limits{
			MaxResultRows:    pol.MaxResultRows,
			MaxResponseBytes: pol.MaxResponseBytes,
		},
		policy: policy.New(policy.Options{
			AggregationOnly:  pol.AggregationOnly,
			AllowedDatabases: pol.AllowedDatabases,
			AllowedTables:    QueryTables(),
		}),
		referDatabase: pol.ReferDatabase,
		timeout:       pol.QueryTimeout,
	}
}

// Name implements gateway.Source.
func (s *Service) Name() string { return "clickhouse" }

// Connect implements gateway.Source.
func (s *Service) Connect(ctx context.Context) error {
	client, err := Dial(ctx, s.cfg, s.timeout, s.logger)
	if err != nil {
		return err
	}
	s.client = client
	return nil
}

// Registrations implements gateway.Source.
func (s *Service) Registrations(context.Context) ([]gateway.Registration, error) {
	if s.client == nil {
		return nil, fmt.Errorf("clickhouse source is not connected")
	}
	queryTool, ok := s.client.Tool(queryToolName)
	if !ok {
		return nil, ErrQueryToolMissing
	}
	return []gateway.Registration{
		{
			Tool:    publicQueryTool(queryTool),
			Handler: s.handleQuery,
		},
		{
			Tool:    schemaCatalogTool(),
			Handler: s.handleSchema,
		},
	}, nil
}

// Instructions implements gateway.Instructor.
func (s *Service) Instructions() string {
	return BriefInstructions() + "\n\n" + LimitInstructions(s.limits.MaxResultRows, s.limits.MaxResponseBytes, s.timeout)
}

func (s *Service) handleSchema(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: CatalogJSON()}}}, nil
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
		s.logger.Warn("invalid_arguments", "tool", queryToolName, "error", err.Error())
		return toolError(err.Error()), nil
	}
	query, _ := args["query"].(string)
	traceID := s.nextTrace()
	if query == "" {
		s.trace(traceID, "rejected", "tool", queryToolName, "reason", "query is required")
		return toolError("query is required and must be a string"), nil
	}
	var params map[string]any
	if raw, ok := args["params"]; ok && raw != nil {
		cast, ok := raw.(map[string]any)
		if !ok {
			s.trace(traceID, "rejected", "tool", queryToolName, "sql", s.sqlForLog(query), "reason", "params must be an object")
			return toolError("params must be a JSON object"), nil
		}
		params = cast
	}
	s.trace(traceID, "received", "tool", queryToolName, "database", s.referDatabase, "sql", s.sqlForLog(query), "param_count", len(params))
	if err := s.policy.Validate(ctx, policy.Query{SQL: query, Params: params}); err != nil {
		s.trace(traceID, "rejected", "tool", queryToolName, "database", s.referDatabase, "sql", s.sqlForLog(query), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	scoped, err := s.authorizeQuery(ctx, req, query)
	if err != nil {
		s.trace(traceID, "rejected", "tool", queryToolName, "database", s.referDatabase, "sql", s.sqlForLog(query), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	if err := s.policy.Validate(ctx, policy.Query{SQL: scoped, Params: params}); err != nil {
		s.trace(traceID, "rejected", "tool", queryToolName, "database", s.referDatabase, "sql", s.sqlForLog(scoped), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	forward := map[string]any{"query": scoped}
	if params != nil {
		forward["params"] = params
	}
	s.trace(traceID, "accepted", "tool", queryToolName, "database", s.referDatabase, "sql", s.sqlForLog(scoped))
	return s.invoke(ctx, queryToolName, traceID, forward)
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
	return policy.ApplyScope(query, bindingsFor(scope), columnsFor())
}

func (s *Service) invoke(ctx context.Context, name, traceID string, args map[string]any) (*mcp.CallToolResult, error) {
	sql := ""
	if name == queryToolName {
		sql, _ = args["query"].(string)
		s.trace(traceID, "upstream", "tool", name, "database", s.referDatabase, "sql", s.sqlForLog(sql))
	}
	start := time.Now()
	result, err := s.client.Call(ctx, name, args)
	elapsed := time.Since(start)
	if err != nil {
		s.trace(traceID, "upstream_error", "tool", name, "database", s.referDatabase, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "error", err.Error())
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
		s.trace(traceID, "result_rejected", "tool", name, "database", s.referDatabase, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "reason", err.Error())
		return toolError(err.Error()), nil
	}
	s.trace(traceID, "completed", "tool", name, "database", s.referDatabase, "sql", s.sqlForLog(sql), "duration_ms", elapsed.Milliseconds(), "is_error", limited.IsError)
	return limited, nil
}

func (s *Service) nextTrace() string {
	return fmt.Sprintf("q%d", s.traceSeq.Add(1))
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
		s.logger.Warn("clickhouse_query", fields...)
		return
	}
	s.logger.Info("clickhouse_query", fields...)
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

package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr         = "127.0.0.1:8080"
	defaultQueryTimeout     = 30
	defaultMaxResultRows    = 200
	defaultMaxResponseBytes = 1 << 20
	defaultReferDatabase    = "lafdatav1"
	defaultPermTimeout      = 5
	TransportStdio          = "stdio"
	TransportHTTP           = "http"
)

// Config is the gateway process configuration.
type Config struct {
	HTTPAddr    string
	ClickHouse  ClickHouse
	Policy      Policy
	Permissions Permissions
	LogSQL      bool
}

// Permissions configures the user-fleet authorization API.
type Permissions struct {
	BaseURL string
	Timeout time.Duration
}

// ClickHouse configures the upstream MCP connection.
// Database credentials belong in Env and are consumed only by the upstream process.
type ClickHouse struct {
	Transport string
	URL       string
	Command   string
	Args      []string
	Env       map[string]string
	AuthToken string
}

// Policy configures query validation and response limits.
type Policy struct {
	QueryTimeout     time.Duration
	MaxResultRows    int
	MaxResponseBytes int
	AggregationOnly  bool
	ReferDatabase    string
	AllowedDatabases []string
}

// LoadFrom reads configuration using getenv.
func LoadFrom(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	cfg := Config{
		HTTPAddr: stringOr(getenv("HTTP_ADDR"), defaultHTTPAddr),
		ClickHouse: ClickHouse{
			Transport: strings.ToLower(stringOr(getenv("CLICKHOUSE_MCP_TRANSPORT"), TransportStdio)),
			URL:       strings.TrimSpace(getenv("CLICKHOUSE_MCP_URL")),
			Command:   strings.TrimSpace(getenv("CLICKHOUSE_MCP_COMMAND")),
			AuthToken: getenv("CLICKHOUSE_MCP_AUTH_TOKEN"),
		},
		LogSQL: boolOr(getenv("LOG_SQL"), false),
	}

	timeoutSec, err := intOr(getenv("QUERY_TIMEOUT_SECONDS"), defaultQueryTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("QUERY_TIMEOUT_SECONDS: %w", err)
	}
	if timeoutSec <= 0 {
		return Config{}, fmt.Errorf("QUERY_TIMEOUT_SECONDS must be positive")
	}
	rows, err := intOr(getenv("MAX_RESULT_ROWS"), defaultMaxResultRows)
	if err != nil {
		return Config{}, fmt.Errorf("MAX_RESULT_ROWS: %w", err)
	}
	if rows <= 0 {
		return Config{}, fmt.Errorf("MAX_RESULT_ROWS must be positive")
	}
	maxBytes, err := intOr(getenv("MAX_RESPONSE_BYTES"), defaultMaxResponseBytes)
	if err != nil {
		return Config{}, fmt.Errorf("MAX_RESPONSE_BYTES: %w", err)
	}
	if maxBytes <= 0 {
		return Config{}, fmt.Errorf("MAX_RESPONSE_BYTES must be positive")
	}
	permTimeout, err := intOr(getenv("PERMISSIONS_TIMEOUT_SECONDS"), defaultPermTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("PERMISSIONS_TIMEOUT_SECONDS: %w", err)
	}
	if permTimeout <= 0 {
		return Config{}, fmt.Errorf("PERMISSIONS_TIMEOUT_SECONDS must be positive")
	}
	permURL := strings.TrimSpace(getenv("PERMISSIONS_API_BASE_URL"))
	parsedPerm, err := url.Parse(permURL)
	if err != nil || parsedPerm.Host == "" || (parsedPerm.Scheme != "http" && parsedPerm.Scheme != "https") || parsedPerm.User != nil {
		return Config{}, fmt.Errorf("PERMISSIONS_API_BASE_URL must be an absolute http or https URL without credentials")
	}
	cfg.Permissions = Permissions{
		BaseURL: strings.TrimRight(permURL, "/"),
		Timeout: time.Duration(permTimeout) * time.Second,
	}

	referDatabase := stringOr(getenv("REFER_DATABASE"), defaultReferDatabase)
	if !validDatabaseName(referDatabase) {
		return Config{}, fmt.Errorf("REFER_DATABASE must be a single ClickHouse identifier")
	}
	cfg.Policy = Policy{
		QueryTimeout:     time.Duration(timeoutSec) * time.Second,
		MaxResultRows:    rows,
		MaxResponseBytes: maxBytes,
		AggregationOnly:  boolOr(getenv("AGGREGATION_ONLY"), false),
		ReferDatabase:    referDatabase,
		AllowedDatabases: []string{referDatabase},
	}

	args, err := parseArgs(getenv("CLICKHOUSE_MCP_ARGS"))
	if err != nil {
		return Config{}, err
	}
	cfg.ClickHouse.Args = args
	env, err := parseEnv(getenv("CLICKHOUSE_MCP_ENV"))
	if err != nil {
		return Config{}, err
	}
	if env == nil {
		env = map[string]string{}
	}
	env["CLICKHOUSE_DATABASE"] = referDatabase
	cfg.ClickHouse.Env = env

	switch cfg.ClickHouse.Transport {
	case TransportStdio:
		if cfg.ClickHouse.Command == "" {
			return Config{}, fmt.Errorf("CLICKHOUSE_MCP_COMMAND is required when CLICKHOUSE_MCP_TRANSPORT=stdio")
		}
	case TransportHTTP:
		if cfg.ClickHouse.URL == "" {
			return Config{}, fmt.Errorf("CLICKHOUSE_MCP_URL is required when CLICKHOUSE_MCP_TRANSPORT=http")
		}
		parsed, err := url.Parse(cfg.ClickHouse.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return Config{}, fmt.Errorf("CLICKHOUSE_MCP_URL must be an absolute http or https URL")
		}
		if parsed.User != nil {
			return Config{}, fmt.Errorf("CLICKHOUSE_MCP_URL must not embed credentials; set CLICKHOUSE_MCP_AUTH_TOKEN")
		}
	default:
		return Config{}, fmt.Errorf("CLICKHOUSE_MCP_TRANSPORT must be stdio or http")
	}
	return cfg, nil
}

func stringOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func intOr(value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", value)
	}
	return n, nil
}

func boolOr(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func validDatabaseName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func parseArgs(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(value), &args); err != nil {
		return nil, fmt.Errorf("CLICKHOUSE_MCP_ARGS must be a JSON array of strings")
	}
	return args, nil
}

func parseEnv(value string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("CLICKHOUSE_MCP_ENV must be a JSON object of strings")
	}
	return raw, nil
}

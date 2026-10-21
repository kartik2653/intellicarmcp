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
	defaultPostgresHost     = "127.0.0.1"
	defaultPostgresPort     = 9471
	defaultPostgresDatabase = "region_india"
	defaultPostgresSSLMode  = "disable"
	TransportStdio          = "stdio"
	TransportHTTP           = "http"
)

// Config is the gateway process configuration.
type Config struct {
	HTTPAddr    string
	ClickHouse  ClickHouse
	Postgres    Postgres
	Policy      Policy
	Permissions Permissions
	LogSQL      bool
}

// Postgres configures the Microsoft PostgreSQL MCP subprocess and AlloyDB target.
type Postgres struct {
	Enabled          bool
	Command          string
	Args             []string
	Env              map[string]string
	Host             string
	Port             int
	Database         string
	User             string
	Password         string
	SSLMode          string
	ProfileName      string
	QueryTimeout     time.Duration
	MaxResultRows    int
	MaxResponseBytes int
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

	if err := loadPostgres(&cfg, getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadPostgres(cfg *Config, getenv func(string) string) error {
	command := strings.TrimSpace(getenv("POSTGRES_MCP_COMMAND"))
	enabledExplicit := strings.TrimSpace(getenv("POSTGRES_MCP_ENABLED"))
	enabled := boolOr(enabledExplicit, command != "")
	if enabledExplicit == "" && command == "" {
		enabled = false
	}
	cfg.Postgres.Enabled = enabled
	if !enabled {
		return nil
	}
	if command == "" {
		command = "npx"
	}
	args, err := parseArgsNamed(getenv("POSTGRES_MCP_ARGS"), "POSTGRES_MCP_ARGS")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"-y", "@microsoft/postgres-mcp", "run"}
	}
	env, err := parseEnvNamed(getenv("POSTGRES_MCP_ENV"), "POSTGRES_MCP_ENV")
	if err != nil {
		return err
	}
	if env == nil {
		env = map[string]string{}
	}
	host := stringOr(getenv("POSTGRES_HOST"), defaultPostgresHost)
	port, err := intOr(getenv("POSTGRES_PORT"), defaultPostgresPort)
	if err != nil {
		return fmt.Errorf("POSTGRES_PORT: %w", err)
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("POSTGRES_PORT must be between 1 and 65535")
	}
	database := stringOr(getenv("POSTGRES_DATABASE"), defaultPostgresDatabase)
	if !validDatabaseName(database) {
		return fmt.Errorf("POSTGRES_DATABASE must be a single PostgreSQL identifier")
	}
	user := strings.TrimSpace(getenv("POSTGRES_USER"))
	if user == "" {
		return fmt.Errorf("POSTGRES_USER is required when PostgreSQL MCP is enabled")
	}
	password := getenv("POSTGRES_PASSWORD")
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("POSTGRES_PASSWORD is required when PostgreSQL MCP is enabled")
	}
	sslMode := stringOr(getenv("POSTGRES_SSLMODE"), defaultPostgresSSLMode)
	switch strings.ToLower(sslMode) {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("POSTGRES_SSLMODE is invalid")
	}
	pgTimeout, err := intOr(getenv("POSTGRES_QUERY_TIMEOUT_SECONDS"), int(cfg.Policy.QueryTimeout/time.Second))
	if err != nil {
		return fmt.Errorf("POSTGRES_QUERY_TIMEOUT_SECONDS: %w", err)
	}
	if pgTimeout <= 0 {
		return fmt.Errorf("POSTGRES_QUERY_TIMEOUT_SECONDS must be positive")
	}
	pgRows, err := intOr(getenv("POSTGRES_MAX_RESULT_ROWS"), cfg.Policy.MaxResultRows)
	if err != nil {
		return fmt.Errorf("POSTGRES_MAX_RESULT_ROWS: %w", err)
	}
	if pgRows <= 0 {
		return fmt.Errorf("POSTGRES_MAX_RESULT_ROWS must be positive")
	}
	pgBytes, err := intOr(getenv("POSTGRES_MAX_RESPONSE_BYTES"), cfg.Policy.MaxResponseBytes)
	if err != nil {
		return fmt.Errorf("POSTGRES_MAX_RESPONSE_BYTES: %w", err)
	}
	if pgBytes <= 0 {
		return fmt.Errorf("POSTGRES_MAX_RESPONSE_BYTES must be positive")
	}
	profile := stringOr(getenv("POSTGRES_MCP_PROFILE_NAME"), "default")
	conn := buildPostgresConnectionString(host, port, user, password, database, sslMode)
	// @microsoft/postgres-mcp reads POSTGRES_MCP_* for the env connection profile.
	env["POSTGRES_MCP_CONNECTION_STRING"] = conn
	env["POSTGRES_MCP_PROFILE_NAME"] = profile
	env["POSTGRES_MCP_QUERY_TIMEOUT_MS"] = strconv.Itoa(pgTimeout * 1000)
	env["POSTGRES_MCP_DISABLE_CWD_ACCESS"] = "1"
	cfg.Postgres = Postgres{
		Enabled:          true,
		Command:          command,
		Args:             args,
		Env:              env,
		Host:             host,
		Port:             port,
		Database:         database,
		User:             user,
		Password:         password,
		SSLMode:          sslMode,
		ProfileName:      profile,
		QueryTimeout:     time.Duration(pgTimeout) * time.Second,
		MaxResultRows:    pgRows,
		MaxResponseBytes: pgBytes,
	}
	return nil
}

func buildPostgresConnectionString(host string, port int, user, password, database, sslMode string) string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + database,
	}
	q := url.Values{}
	q.Set("sslmode", sslMode)
	u.RawQuery = q.Encode()
	return u.String()
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
	return parseArgsNamed(value, "CLICKHOUSE_MCP_ARGS")
}

func parseEnv(value string) (map[string]string, error) {
	return parseEnvNamed(value, "CLICKHOUSE_MCP_ENV")
}

func parseArgsNamed(value, name string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(value), &args); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of strings", name)
	}
	return args, nil
}

func parseEnvNamed(value, name string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of strings", name)
	}
	return raw, nil
}

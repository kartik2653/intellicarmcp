package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://api.x.ai/v1"
	defaultModel      = "grok-4.6"
	defaultMCPURL     = "http://127.0.0.1:8080/mcp"
	defaultMaxCalls   = 10
	defaultTimeoutSec = 60
	defaultDatabase   = "lafdatav1"
)

// Config is the CLI process configuration.
type Config struct {
	APIKey         string
	BaseURL        string
	Model          string
	MCPServerURL   string
	MaxToolCalls   int
	RequestTimeout time.Duration
	ReferDatabase  string
}

// LoadFrom reads configuration using getenv.
func LoadFrom(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	apiKey := strings.TrimSpace(getenv("XAI_API_KEY"))
	if apiKey == "" {
		return Config{}, fmt.Errorf("XAI_API_KEY is required")
	}
	baseURL := strings.TrimRight(stringOr(getenv("XAI_BASE_URL"), defaultBaseURL), "/")
	parsedBase, err := url.Parse(baseURL)
	if err != nil || parsedBase.Host == "" || (parsedBase.Scheme != "http" && parsedBase.Scheme != "https") {
		return Config{}, fmt.Errorf("XAI_BASE_URL must be an absolute http or https URL")
	}
	if parsedBase.User != nil {
		return Config{}, fmt.Errorf("XAI_BASE_URL must not embed credentials")
	}
	model := stringOr(getenv("XAI_MODEL"), defaultModel)
	mcpURL := stringOr(getenv("MCP_SERVER_URL"), defaultMCPURL)
	parsedMCP, err := url.Parse(mcpURL)
	if err != nil || parsedMCP.Host == "" || (parsedMCP.Scheme != "http" && parsedMCP.Scheme != "https") {
		return Config{}, fmt.Errorf("MCP_SERVER_URL must be an absolute http or https URL")
	}
	if parsedMCP.User != nil {
		return Config{}, fmt.Errorf("MCP_SERVER_URL must not embed credentials")
	}
	maxCalls, err := intOr(getenv("MAX_TOOL_CALLS"), defaultMaxCalls)
	if err != nil {
		return Config{}, fmt.Errorf("MAX_TOOL_CALLS: %w", err)
	}
	if maxCalls <= 0 {
		return Config{}, fmt.Errorf("MAX_TOOL_CALLS must be positive")
	}
	timeoutSec, err := intOr(getenv("REQUEST_TIMEOUT_SECONDS"), defaultTimeoutSec)
	if err != nil {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS: %w", err)
	}
	if timeoutSec <= 0 {
		return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be positive")
	}
	referDatabase := stringOr(getenv("REFER_DATABASE"), defaultDatabase)
	if !validDatabaseName(referDatabase) {
		return Config{}, fmt.Errorf("REFER_DATABASE must be a single ClickHouse identifier")
	}
	return Config{
		APIKey:         apiKey,
		BaseURL:        baseURL,
		Model:          model,
		MCPServerURL:   mcpURL,
		MaxToolCalls:   maxCalls,
		RequestTimeout: time.Duration(timeoutSec) * time.Second,
		ReferDatabase:  referDatabase,
	}, nil
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

package authz

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Fetcher loads a user's analytics scope from the bearer token.
type Fetcher interface {
	Fetch(ctx context.Context, bearer string) (Scope, error)
}

// Client calls the fleet-permissions endpoint with the caller's bearer token.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Fetch loads the authoritative scope for the bearer token.
func (c *Client) Fetch(ctx context.Context, bearer string) (Scope, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return Scope{}, fmt.Errorf("permissions API is not configured")
	}
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return Scope{}, fmt.Errorf("missing bearer token")
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/v1/permissions/userfleetvehicles"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Scope{}, fmt.Errorf("permissions request failed")
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Scope{}, ctx.Err()
		}
		return Scope{}, fmt.Errorf("permissions request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Scope{}, fmt.Errorf("permissions response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Scope{}, fmt.Errorf("permissions API returned HTTP %d", resp.StatusCode)
	}
	return ParseScope(body)
}

// BearerToken returns the single bearer token from an HTTP header.
func BearerToken(header http.Header) (string, error) {
	if header == nil {
		return "", fmt.Errorf("missing bearer token")
	}
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", fmt.Errorf("missing bearer token")
	}
	fields := strings.Fields(values[0])
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") || strings.TrimSpace(fields[1]) == "" {
		return "", fmt.Errorf("missing bearer token")
	}
	return fields[1], nil
}

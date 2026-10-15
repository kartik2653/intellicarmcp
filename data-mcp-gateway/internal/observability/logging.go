package observability

import (
	"io"
	"log/slog"
	"net/url"
	"strings"
	"unicode"
)

// New returns a JSON logger that writes to w.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// SecretEnvKey reports whether an environment variable name is likely to hold a credential.
func SecretEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, part := range []string{"PASSWORD", "TOKEN", "SECRET", "KEY"} {
		if strings.Contains(upper, part) {
			return true
		}
	}
	return false
}

// RedactURL returns a URL without userinfo. Invalid URLs are returned as an empty string.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// RedactSQL replaces quoted string literals with placeholders.
// It is only for debug logs. It is not a security control.
func RedactSQL(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inLineComment {
			b.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(c)
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				b.WriteByte('/')
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			if c == '\\' && i+1 < len(sql) {
				i++
				continue
			}
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
				b.WriteString("?")
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
				b.WriteByte(c)
			}
			continue
		}
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			inLineComment = true
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			inBlockComment = true
			b.WriteByte(c)
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			b.WriteByte(c)
			continue
		}
		b.WriteByte(c)
	}
	if inSingle {
		b.WriteString("?")
	}
	return strings.TrimRightFunc(b.String(), unicode.IsSpace)
}

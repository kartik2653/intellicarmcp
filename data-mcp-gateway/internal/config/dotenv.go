package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

const defaultEnvFile = ".env"

// Load reads .env, then fills any missing keys from the process environment.
// Values found in the file are not written back with os.Setenv.
func Load() (Config, error) {
	fileValues, err := loadEnvFile(os.Getenv("GATEWAY_ENV_FILE"))
	if err != nil {
		return Config{}, err
	}
	return LoadFrom(func(key string) string {
		if value, ok := fileValues[key]; ok {
			return value
		}
		return os.Getenv(key)
	})
}

func loadEnvFile(path string) (map[string]string, error) {
	explicit := strings.TrimSpace(path) != ""
	if !explicit {
		path = defaultEnvFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !explicit {
			return map[string]string{}, nil
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("GATEWAY_ENV_FILE %s does not exist", path)
		}
		return nil, fmt.Errorf("read env file: %w", err)
	}
	values, err := parseEnvFile(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return values, nil
}

func parseEnvFile(data []byte) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t#") {
			return nil, fmt.Errorf("line %d has an invalid key", lineNo)
		}
		parsed, err := parseEnvValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		values[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '"':
		return unquoteDouble(value)
	case '\'':
		return unquoteSingle(value)
	default:
		if idx := strings.Index(value, " #"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		return value, nil
	}
}

func unquoteDouble(value string) (string, error) {
	if len(value) < 2 || !strings.HasSuffix(value, "\"") {
		return "", fmt.Errorf("unterminated double quote")
	}
	var b strings.Builder
	escaped := false
	for _, r := range value[1 : len(value)-1] {
		if escaped {
			switch r {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteRune(r)
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		return "", fmt.Errorf("unterminated escape")
	}
	return b.String(), nil
}

func unquoteSingle(value string) (string, error) {
	if len(value) < 2 || !strings.HasSuffix(value, "'") {
		return "", fmt.Errorf("unterminated single quote")
	}
	return value[1 : len(value)-1], nil
}

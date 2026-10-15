package mcpclient

import (
	"encoding/json"
	"fmt"

	"github.com/Intellicars/intellicarmcp/analytics-mcp-client/internal/llm"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var objectParameters = json.RawMessage(`{"type":"object","additionalProperties":true}`)

// Convert builds an xAI function definition from a discovered MCP tool.
// A schema whose root is not an object is replaced for the model with an
// object schema. Local validation still uses the original schema when it resolves.
func Convert(tool *mcp.Tool) (llm.Tool, json.RawMessage, error) {
	if tool == nil || tool.Name == "" {
		return llm.Tool{}, nil, fmt.Errorf("mcp tool is missing a name")
	}
	raw, err := marshalSchema(tool.InputSchema)
	if err != nil {
		return llm.Tool{}, nil, fmt.Errorf("tool %s schema: %w", tool.Name, err)
	}
	parameters := raw
	if !objectRoot(raw) {
		parameters = objectParameters
	}
	return llm.Tool{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  append(json.RawMessage(nil), parameters...),
		},
	}, raw, nil
}

// Validate checks JSON arguments against a JSON schema.
func Validate(schema, arguments json.RawMessage) error {
	if len(bytesTrim(arguments)) == 0 {
		arguments = []byte("{}")
	}
	var instance any
	if err := json.Unmarshal(arguments, &instance); err != nil {
		return fmt.Errorf("tool arguments must be valid JSON")
	}
	if _, ok := instance.(map[string]any); !ok {
		return fmt.Errorf("tool arguments must be a JSON object")
	}
	if len(bytesTrim(schema)) == 0 || !objectRoot(schema) {
		return nil
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return fmt.Errorf("tool input schema could not be parsed")
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return fmt.Errorf("tool input schema could not be resolved")
	}
	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("tool arguments do not match the input schema")
	}
	return nil
}

func marshalSchema(schema any) (json.RawMessage, error) {
	if schema == nil {
		return append(json.RawMessage(nil), objectParameters...), nil
	}
	switch typed := schema.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return append(json.RawMessage(nil), objectParameters...), nil
		}
		return append(json.RawMessage(nil), typed...), nil
	case []byte:
		if len(typed) == 0 {
			return append(json.RawMessage(nil), objectParameters...), nil
		}
		return append(json.RawMessage(nil), typed...), nil
	default:
		raw, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
}

func objectRoot(schema json.RawMessage) bool {
	if len(bytesTrim(schema)) == 0 {
		return false
	}
	var doc any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return false
	}
	return schemaRootIsObject(doc)
}

func schemaRootIsObject(doc any) bool {
	object, ok := doc.(map[string]any)
	if !ok {
		return false
	}
	switch typed := object["type"].(type) {
	case string:
		if typed == "object" {
			return true
		}
	case []any:
		for _, item := range typed {
			if value, ok := item.(string); ok && value == "object" {
				return true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		branches, ok := object[key].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		all := true
		for _, branch := range branches {
			if !schemaRootIsObject(branch) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func bytesTrim(raw []byte) []byte {
	return []byte(trimSpace(string(raw)))
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\t' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\t' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

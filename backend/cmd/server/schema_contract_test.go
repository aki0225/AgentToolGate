package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"agenttoolgate/backend/internal/guard"
)

func TestGeneratedProjectDocumentsMatchSchemaContracts(t *testing.T) {
	root := repositoryRootForSchemaTest(t)
	configSchema := readSchemaContract(t, filepath.Join(root, "schemas", "project-config.schema.json"))
	protectedSchema := readSchemaContract(t, filepath.Join(root, "schemas", "project-protection.schema.json"))

	cfg := defaultProjectRunConfig(t.TempDir())
	assertJSONMatchesSchemaContract(t, []byte(renderProjectConfigFile(cfg)), configSchema, "$")
	assertJSONMatchesSchemaContract(t, []byte(renderProjectProtectedFile(cfg)), protectedSchema, "$")

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	if err := os.WriteFile(projectConfigPath(project), []byte(renderProjectConfigFile(cfg)), 0o600); err != nil {
		t.Fatalf("write generated config: %v", err)
	}
	if err := os.WriteFile(projectProtectedPath(project), []byte(renderProjectProtectedFile(cfg)), 0o600); err != nil {
		t.Fatalf("write generated protection: %v", err)
	}
	if _, _, _, err := loadProjectRunConfig(project); err != nil {
		t.Fatalf("generated config must satisfy the runtime loader: %v", err)
	}
	if _, err := guard.LoadProjectProtection(project); err != nil {
		t.Fatalf("generated protection must satisfy the runtime loader: %v", err)
	}
}

func TestProjectSchemasDeclareStrictRuntimeBoundaries(t *testing.T) {
	root := repositoryRootForSchemaTest(t)
	configSchema := readSchemaContract(t, filepath.Join(root, "schemas", "project-config.schema.json"))
	protectedSchema := readSchemaContract(t, filepath.Join(root, "schemas", "project-protection.schema.json"))

	for _, path := range [][]string{
		{"properties", "projectRoot"},
		{"properties", "host"},
		{"properties", "workspace", "properties", "name"},
		{"properties", "workspace", "properties", "slug"},
		{"properties", "workspace", "properties", "orgId"},
	} {
		assertSchemaStringBoundary(t, schemaObjectAt(t, configSchema, path...), 1, 0, true)
	}
	for _, path := range [][]string{
		{"properties", "projectRoot"},
		{"properties", "workspace", "properties", "name"},
		{"properties", "workspace", "properties", "slug"},
		{"properties", "workspace", "properties", "orgId"},
	} {
		assertSchemaStringBoundary(t, schemaObjectAt(t, protectedSchema, path...), 1, 0, true)
	}
	assertSchemaStringBoundary(t, schemaObjectAt(t, protectedSchema,
		"properties", "localActionFirewall", "properties", "protectedPaths", "items", "properties", "pattern",
	), 1, 256, true)
	assertSchemaStringBoundary(t, schemaObjectAt(t, protectedSchema,
		"properties", "localActionFirewall", "properties", "protectedPaths", "items", "properties", "reason",
	), 0, 160, true)
}

func repositoryRootForSchemaTest(t *testing.T) string {
	t.Helper()
	current, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	root := filepath.Clean(filepath.Join(current, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "backend", "go.mod")); err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return root
}

func readSchemaContract(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema %s: %v", path, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema %s: %v", path, err)
	}
	return schema
}

func assertJSONMatchesSchemaContract(t *testing.T, raw []byte, schema map[string]any, path string) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("%s generated JSON invalid: %v", path, err)
	}
	assertValueMatchesSchemaContract(t, value, schema, schema, path)
}

func assertValueMatchesSchemaContract(t *testing.T, value any, schema, rootSchema map[string]any, path string) {
	t.Helper()
	if reference, ok := schema["$ref"].(string); ok {
		schema = resolveLocalSchemaReference(t, rootSchema, reference)
	}
	if constant, exists := schema["const"]; exists && constant != value {
		t.Fatalf("%s value %v does not equal const %v", path, value, constant)
	}
	if enumValues, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enumValues {
			if candidate == value {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s value %v is outside enum %v", path, value, enumValues)
		}
	}
	if object, ok := value.(map[string]any); ok {
		for _, required := range schemaStringList(schema["required"]) {
			if _, exists := object[required]; !exists {
				t.Fatalf("%s missing required property %s", path, required)
			}
		}
		if alternatives, ok := schema["anyOf"].([]any); ok {
			matched := false
			for _, alternative := range alternatives {
				candidate, ok := alternative.(map[string]any)
				if ok && schemaConditionMatches(value, candidate, rootSchema) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("%s does not satisfy any anyOf branch", path)
			}
		}
	}
	if clauses, ok := schema["allOf"].([]any); ok {
		for _, clauseValue := range clauses {
			clause, ok := clauseValue.(map[string]any)
			if !ok {
				t.Fatalf("%s allOf clause is not an object", path)
			}
			condition, hasCondition := clause["if"].(map[string]any)
			thenSchema, hasThen := clause["then"].(map[string]any)
			if hasCondition && hasThen && schemaConditionMatches(value, condition, rootSchema) {
				assertValueMatchesSchemaContract(t, value, thenSchema, rootSchema, path)
			}
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s must be an object, got %T", path, value)
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, child := range object {
			rawChildSchema, exists := properties[key]
			if !exists {
				if schema["additionalProperties"] == false {
					t.Fatalf("%s contains unknown property %s", path, key)
				}
				continue
			}
			childSchema, ok := rawChildSchema.(map[string]any)
			if !ok {
				t.Fatalf("%s.%s schema is not an object", path, key)
			}
			assertValueMatchesSchemaContract(t, child, childSchema, rootSchema, path+"."+key)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			t.Fatalf("%s must be an array, got %T", path, value)
		}
		if maximum, ok := schema["maxItems"].(float64); ok && len(items) > int(maximum) {
			t.Fatalf("%s must contain at most %v items", path, maximum)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range items {
			assertValueMatchesSchemaContract(t, item, itemSchema, rootSchema, path+"["+strconv.Itoa(index)+"]")
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			t.Fatalf("%s must be a string, got %T", path, value)
		}
		length := utf8.RuneCountInString(text)
		if minimum, ok := schema["minLength"].(float64); ok && length < int(minimum) {
			t.Fatalf("%s must contain at least %v characters", path, minimum)
		}
		if maximum, ok := schema["maxLength"].(float64); ok && length > int(maximum) {
			t.Fatalf("%s must contain at most %v characters", path, maximum)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			t.Fatalf("%s must be an integer, got %v", path, value)
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			t.Fatalf("%s must be at least %v", path, minimum)
		}
		if maximum, ok := schema["maximum"].(float64); ok && number > maximum {
			t.Fatalf("%s must be at most %v", path, maximum)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			t.Fatalf("%s must be a boolean, got %T", path, value)
		}
	}
}

func schemaConditionMatches(value any, schema, rootSchema map[string]any) bool {
	if reference, ok := schema["$ref"].(string); ok {
		resolved := rootSchema
		for _, segment := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
			next, ok := resolved[segment].(map[string]any)
			if !ok {
				return false
			}
			resolved = next
		}
		schema = resolved
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, required := range schemaStringList(schema["required"]) {
		if _, exists := object[required]; !exists {
			return false
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, rawProperty := range properties {
		property, exists := object[name]
		if !exists {
			continue
		}
		propertySchema, ok := rawProperty.(map[string]any)
		if !ok {
			return false
		}
		if constant, exists := propertySchema["const"]; exists && constant != property {
			return false
		}
	}
	return true
}

func schemaObjectAt(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	var current any = root
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("schema path %s traverses a non-object", strings.Join(path, "."))
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("schema path %s is missing %s", strings.Join(path, "."), segment)
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("schema path %s does not resolve to an object", strings.Join(path, "."))
	}
	return object
}

func assertSchemaStringBoundary(t *testing.T, schema map[string]any, minLength, maxLength int, requirePattern bool) {
	t.Helper()
	if schema["type"] != "string" {
		t.Fatalf("schema must describe a string, got %v", schema["type"])
	}
	if minLength > 0 && schema["minLength"] != float64(minLength) {
		t.Fatalf("schema minLength got %v want %d", schema["minLength"], minLength)
	}
	if maxLength > 0 && schema["maxLength"] != float64(maxLength) {
		t.Fatalf("schema maxLength got %v want %d", schema["maxLength"], maxLength)
	}
	if requirePattern {
		pattern, ok := schema["pattern"].(string)
		if !ok || pattern == "" {
			t.Fatal("schema must declare a non-empty pattern")
		}
	}
}

func resolveLocalSchemaReference(t *testing.T, rootSchema map[string]any, reference string) map[string]any {
	t.Helper()
	const prefix = "#/"
	if len(reference) <= len(prefix) || reference[:len(prefix)] != prefix {
		t.Fatalf("unsupported schema reference %q", reference)
	}
	var current any = rootSchema
	for _, segment := range strings.Split(reference[len(prefix):], "/") {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("schema reference %q traverses a non-object", reference)
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("schema reference %q is missing segment %q", reference, segment)
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("schema reference %q does not resolve to an object", reference)
	}
	return resolved
}

func schemaStringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

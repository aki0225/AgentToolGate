package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"agenttoolgate/backend/internal/model"
)

var (
	secretNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	secretValueRefPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	allowedSecretTypes      = map[string]struct{}{"env": {}, "text": {}, "token": {}, "api_key": {}, "oauth_like": {}, "generic": {}}
	allowedSecretValueSource = map[string]struct{}{"env": {}}
)

func NormalizeSecretName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", fmt.Errorf("secret name is required")
	}
	if !secretNamePattern.MatchString(name) {
		return "", fmt.Errorf("secret name is invalid")
	}
	return name, nil
}

func NormalizeSecretType(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func ValidateSecretType(secretType string) error {
	if _, ok := allowedSecretTypes[NormalizeSecretType(secretType)]; ok {
		return nil
	}
	return fmt.Errorf("secret type is invalid")
}

func NormalizeSecretValueSource(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func ValidateSecretValueSource(valueSource string) error {
	if _, ok := allowedSecretValueSource[NormalizeSecretValueSource(valueSource)]; ok {
		return nil
	}
	return fmt.Errorf("secret valueSource is invalid")
}

func NormalizeSecretValueRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", fmt.Errorf("secret valueRef is required")
	}
	if !secretValueRefPattern.MatchString(ref) {
		return "", fmt.Errorf("secret valueRef is invalid")
	}
	return ref, nil
}

func NormalizeSecretMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("secret metadata must be valid JSON")
	}
	if decoded == nil {
		return json.RawMessage(`{}`), nil
	}
	if _, ok := decoded.(map[string]any); !ok {
		return nil, fmt.Errorf("secret metadata must be an object")
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func CloneSecretJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	out := make([]byte, len(value))
	copy(out, value)
	return json.RawMessage(out)
}

func CloneSecretBindings(bindings []model.SecretBinding) []model.SecretBinding {
	if len(bindings) == 0 {
		return nil
	}
	cloned := make([]model.SecretBinding, len(bindings))
	copy(cloned, bindings)
	return cloned
}

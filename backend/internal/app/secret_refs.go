package app

import (
	"regexp"
	"strings"
)

var secretReferenceValuePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func isSecretReferenceJSONKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	if normalized == "headersecretrefs" {
		return true
	}
	return strings.HasSuffix(normalized, "secretref") || strings.HasSuffix(normalized, "secretrefs")
}

func isValidSecretReferenceValue(value string) bool {
	return secretReferenceValuePattern.MatchString(strings.TrimSpace(value))
}

func normalizeSecretReferenceValue(value string) string {
	return strings.TrimSpace(value)
}

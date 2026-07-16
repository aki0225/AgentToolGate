package app

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	outputEmailValueRegex      = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	outputPhoneValueRegex      = regexp.MustCompile(`(?i)\+?\d[\d\s().-]{6,}\d`)
	outputPrivateKeyValueRegex = regexp.MustCompile(`(?s)-----BEGIN [^-]+-----.*?-----END [^-]+-----`)
	outputTokenValueRegex      = regexp.MustCompile(`[A-Za-z0-9._=\-]{16,}`)
)

func redactToolOutputForAudit(toolKey string, value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	if strings.EqualFold(strings.TrimSpace(toolKey), "database.query") {
		return defaultJSON(value)
	}
	if namespace, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(toolKey)), "."); ok && strings.HasPrefix(namespace, "mcp_") {
		return redactMCPToolOutputForAudit(value)
	}

	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return defaultJSON(value)
	}

	redacted := redactJSONValueByValue(redactJSONValueByKey(decoded))
	raw, err := json.Marshal(redacted)
	if err != nil {
		return defaultJSON(value)
	}
	return raw
}

func redactJSONValueByValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			redacted[key] = redactJSONValueByValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactJSONValueByValue(item)
		}
		return redacted
	case string:
		return redactSensitiveStringValues(typed)
	default:
		return value
	}
}

func redactSensitiveStringValues(value string) string {
	redacted := outputPrivateKeyValueRegex.ReplaceAllString(value, "[REDACTED]")
	redacted = outputEmailValueRegex.ReplaceAllStringFunc(redacted, maskEmailValue)
	redacted = outputPhoneValueRegex.ReplaceAllStringFunc(redacted, maskPhoneValue)
	redacted = outputTokenValueRegex.ReplaceAllStringFunc(redacted, func(match string) string {
		if looksSensitiveToken(match) {
			return maskGenericSecret(match)
		}
		return match
	})
	return redacted
}

func maskEmailValue(value string) string {
	local, domain, ok := strings.Cut(value, "@")
	if !ok || strings.TrimSpace(local) == "" || strings.TrimSpace(domain) == "" {
		return maskGenericSecret(value)
	}

	localRune := firstRune(local)
	if localRune == "" {
		return "[REDACTED]"
	}
	return localRune + "***@" + domain
}

func maskPhoneValue(value string) string {
	digits := extractDigits(value)
	if len(digits) < 7 {
		return "[REDACTED]"
	}

	prefixLen := 2
	if len(digits) < prefixLen {
		prefixLen = len(digits)
	}
	suffixLen := 4
	if len(digits) < prefixLen+suffixLen {
		suffixLen = len(digits) - prefixLen
	}
	if suffixLen < 2 {
		return "[REDACTED]"
	}

	masked := digits[:prefixLen] + "***" + digits[len(digits)-suffixLen:]
	if strings.HasPrefix(strings.TrimSpace(value), "+") {
		return "+" + masked
	}
	return masked
}

func looksSensitiveToken(value string) bool {
	if strings.Contains(strings.ToUpper(value), "PRIVATE KEY") {
		return true
	}

	hasLetter := false
	hasDigit := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			hasLetter = true
		case r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasLetter && hasDigit && len(value) >= 16
}

func maskGenericSecret(value string) string {
	runes := []rune(value)
	if len(runes) <= 6 {
		return "[REDACTED]"
	}
	return string(runes[:3]) + "***" + string(runes[len(runes)-3:])
}

func firstRune(value string) string {
	for _, r := range value {
		return string(r)
	}
	return ""
}

func extractDigits(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

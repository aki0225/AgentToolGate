package app

import (
	"fmt"
	"sort"
	"strings"
)

type resolvedSecretValueReplacer struct {
	values []string
}

func redactResolvedSecretValues(value any, secretValues []string) any {
	replacer := newResolvedSecretValueReplacer(secretValues)
	if replacer == nil {
		return value
	}
	return replaceResolvedSecretValues(value, replacer)
}

func newResolvedSecretValueReplacer(secretValues []string) *resolvedSecretValueReplacer {
	unique := make(map[string]struct{}, len(secretValues))
	values := make([]string, 0, len(secretValues))
	for _, value := range secretValues {
		if value == "" {
			continue
		}
		if _, exists := unique[value]; exists {
			continue
		}
		unique[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil
	}

	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) == len(values[j]) {
			return values[i] < values[j]
		}
		return len(values[i]) > len(values[j])
	})
	return &resolvedSecretValueReplacer{values: values}
}

func (r *resolvedSecretValueReplacer) Replace(value string) string {
	if r == nil || value == "" {
		return value
	}
	var redacted strings.Builder
	redacted.Grow(len(value))
	for offset := 0; offset < len(value); {
		matched := ""
		for _, secret := range r.values {
			if strings.HasPrefix(value[offset:], secret) {
				matched = secret
				break
			}
		}
		if matched != "" {
			redacted.WriteString("[REDACTED]")
			offset += len(matched)
			continue
		}
		redacted.WriteByte(value[offset])
		offset++
	}
	return redacted.String()
}

func replaceResolvedSecretValues(value any, replacer *resolvedSecretValueReplacer) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		usedKeys := make(map[string]struct{}, len(typed))
		for _, key := range sortedStringMapKeys(typed) {
			redactedKey := uniqueRedactedMapKey(replacer.Replace(key), usedKeys)
			redacted[redactedKey] = replaceResolvedSecretValues(typed[key], replacer)
		}
		return redacted
	case map[string]string:
		redacted := make(map[string]string, len(typed))
		usedKeys := make(map[string]struct{}, len(typed))
		for _, key := range sortedStringMapKeys(typed) {
			redactedKey := uniqueRedactedMapKey(replacer.Replace(key), usedKeys)
			redacted[redactedKey] = replacer.Replace(typed[key])
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = replaceResolvedSecretValues(item, replacer)
		}
		return redacted
	case []string:
		redacted := make([]string, len(typed))
		for index, item := range typed {
			redacted[index] = replacer.Replace(item)
		}
		return redacted
	case string:
		return replacer.Replace(typed)
	default:
		return value
	}
}

func sortedStringMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueRedactedMapKey(candidate string, used map[string]struct{}) string {
	if _, exists := used[candidate]; !exists {
		used[candidate] = struct{}{}
		return candidate
	}
	for suffix := 2; ; suffix++ {
		key := fmt.Sprintf("%s#%d", candidate, suffix)
		if _, exists := used[key]; !exists {
			used[key] = struct{}{}
			return key
		}
	}
}

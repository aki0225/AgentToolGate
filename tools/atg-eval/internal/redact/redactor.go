package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const RedactedValue = "[REDACTED]"

type PathReplacement struct {
	Path        string
	Replacement string
}

type Options struct {
	Secrets []string
	Paths   []PathReplacement
}

type Redactor struct {
	secrets []string
	paths   []PathReplacement
}

var (
	authorizationPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._~+/=-]+`)
	querySecretPattern   = regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|api_key|apikey|token|secret|password|passwd|auth|signature|cookie)=)[^&#\s]+`)
	dsnPasswordPattern   = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/@\s]+:)[^@/\s]+(@)`)
	assignmentPattern    = regexp.MustCompile(`(?i)\b(authorization|token|access_token|refresh_token|api_key|apikey|secret|password|passwd|client_secret|cookie|signature|fingerprint|approval_id)\b(\s*[:=]\s*)(["']?)[^"'\s,;}]+(["']?)`)
)

func New(options Options) *Redactor {
	secrets := compactSorted(options.Secrets)
	paths := make([]PathReplacement, 0, len(options.Paths)*3)
	seenPaths := make(map[string]struct{})
	for _, item := range options.Paths {
		path := strings.TrimSpace(item.Path)
		if path == "" {
			continue
		}
		replacement := strings.TrimSpace(item.Replacement)
		if replacement == "" {
			replacement = "<path>"
		}
		for _, variant := range []string{path, filepath.ToSlash(path), filepath.FromSlash(path)} {
			if variant == "" {
				continue
			}
			key := strings.ToLower(variant)
			if _, exists := seenPaths[key]; exists {
				continue
			}
			seenPaths[key] = struct{}{}
			paths = append(paths, PathReplacement{Path: variant, Replacement: replacement})
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return len(paths[i].Path) > len(paths[j].Path)
	})
	return &Redactor{secrets: secrets, paths: paths}
}

func (r *Redactor) Text(value string) string {
	redacted := value
	for _, secret := range r.secrets {
		redacted = strings.ReplaceAll(redacted, secret, RedactedValue)
	}
	for _, item := range r.paths {
		redacted = replaceFold(redacted, item.Path, item.Replacement)
	}
	redacted = authorizationPattern.ReplaceAllString(redacted, `$1 `+RedactedValue)
	redacted = querySecretPattern.ReplaceAllString(redacted, `${1}`+RedactedValue)
	redacted = dsnPasswordPattern.ReplaceAllString(redacted, `${1}`+RedactedValue+`${2}`)
	redacted = assignmentPattern.ReplaceAllString(redacted, `${1}${2}${3}`+RedactedValue+`${4}`)
	return redacted
}

// JSON 对敏感 key 的完整值做替换，并继续处理普通字符串中的凭据片段。
// 无效 JSON 必须返回错误，禁止把未经处理的原文作为降级结果。
func (r *Redactor) JSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("解析待脱敏 JSON 失败：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("待脱敏内容只能包含一个 JSON 值")
		}
		return nil, fmt.Errorf("待脱敏 JSON 尾部无效：%w", err)
	}
	return json.Marshal(r.redactValue(value))
}

func (r *Redactor) Headers(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if SensitiveKey(key) {
			result[key] = RedactedValue
			continue
		}
		redactedValues := make([]string, 0, len(values))
		for _, value := range values {
			redactedValues = append(redactedValues, r.Text(value))
		}
		result[key] = strings.Join(redactedValues, ", ")
	}
	return result
}

func (r *Redactor) ContainsSensitiveText(value string) bool {
	return r.Text(value) != value
}

func SensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		default:
			return r
		}
	}, strings.ToLower(strings.TrimSpace(key)))

	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie",
		"token", "accesstoken", "refreshtoken", "apikey", "secret",
		"password", "passwd", "privatekey", "clientsecret", "dsn",
		"databaseurl", "signature", "fingerprint", "approvalid":
		return true
	}
	return strings.HasSuffix(normalized, "token") ||
		strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "privatekey") ||
		strings.HasSuffix(normalized, "clientsecret")
}

func (r *Redactor) redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if SensitiveKey(key) {
				result[key] = RedactedValue
				continue
			}
			result[key] = r.redactValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = r.redactValue(item)
		}
		return result
	case string:
		return r.Text(typed)
	default:
		return typed
	}
}

func compactSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return len(result[i]) > len(result[j])
	})
	return result
}

func replaceFold(value, old, replacement string) string {
	if old == "" {
		return value
	}
	lowerValue := strings.ToLower(value)
	lowerOld := strings.ToLower(old)
	var builder strings.Builder
	for {
		index := strings.Index(lowerValue, lowerOld)
		if index < 0 {
			builder.WriteString(value)
			return builder.String()
		}
		builder.WriteString(value[:index])
		builder.WriteString(replacement)
		value = value[index+len(old):]
		lowerValue = lowerValue[index+len(old):]
	}
}

package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvedSecretRedactionDoesNotRequireTokenBoundaries(t *testing.T) {
	input := map[string]any{
		"joined":             "prefixxySuffix",
		"multiple":           "left-orchid-right-xy-tail",
		"key-prefix-xy-tail": "value-prefix-orchid-tail",
		"nested": []any{
			"axyz",
			map[string]any{"orchidStatus": "beforeorchidafter"},
		},
	}

	redacted := redactResolvedSecretValues(input, []string{"xy", "orchid"})
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted value: %v", err)
	}
	for _, secret := range []string{"xy", "orchid"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("resolved Secret %q leaked when joined to surrounding characters: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %s", raw)
	}
}

func TestResolvedSecretRedactionPrefersLongestOverlappingValue(t *testing.T) {
	redacted, ok := redactResolvedSecretValues(
		"prefix-token-secret-suffix",
		[]string{"token", "token-secret"},
	).(string)
	if !ok {
		t.Fatal("expected redacted string")
	}
	if redacted != "prefix-[REDACTED]-suffix" {
		t.Fatalf("expected longest resolved Secret to be replaced once, got %q", redacted)
	}
}

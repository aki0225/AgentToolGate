package hookassets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexBundleMatchesProductHooks(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	tests := []struct {
		name     string
		path     string
		embedded []byte
	}{
		{
			name:     "adapter",
			path:     filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"),
			embedded: Codex().Adapter,
		},
		{
			name:     "core",
			path:     filepath.Join(repoRoot, ".claude", "hooks", "_guard_core.py"),
			embedded: Codex().Core,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read product hook %s: %v", tc.path, err)
			}
			if !bytes.Equal(tc.embedded, want) {
				t.Fatalf("embedded %s differs from product hook %s", tc.name, tc.path)
			}
		})
	}
}

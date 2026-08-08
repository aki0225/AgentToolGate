package report

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeLocationRejectsOverlapAndExistingOutput(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name        string
		output      string
		sandboxBase string
	}{
		{"same", filepath.Join(base, "same"), filepath.Join(base, "same")},
		{"output inside sandbox", filepath.Join(base, "sandbox", "proof"), filepath.Join(base, "sandbox")},
		{"sandbox inside output", filepath.Join(base, "proof"), filepath.Join(base, "proof", "sandbox")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeLocation(test.output, test.sandboxBase); err == nil {
				t.Fatal("重叠路径必须被拒绝")
			}
		})
	}

	existing := filepath.Join(base, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := NormalizeLocation(existing, filepath.Join(base, "sandbox-base")); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("已存在 output 必须返回 ErrOutputExists，error=%v", err)
	}
}

func TestNormalizeLocationRejectsSymlinkedOutputParent(t *testing.T) {
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	linkParent := filepath.Join(base, "linked-parent")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("当前平台无法创建测试符号链接：%v", err)
	}
	if _, err := NormalizeLocation(
		filepath.Join(linkParent, "proof"),
		filepath.Join(base, "sandbox"),
	); err == nil {
		t.Fatal("经过符号链接的 output 父目录必须被拒绝")
	}
}

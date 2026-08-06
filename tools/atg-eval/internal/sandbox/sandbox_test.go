package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateResolveAndCleanup(t *testing.T) {
	base := filepath.Join(t.TempDir(), "evaluation")
	root, err := Create(base, "run-001")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	outside := filepath.Join(filepath.Dir(base), "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	target, err := root.ResolveTarget("<sandbox>/workspace/file.txt")
	if err != nil {
		t.Fatalf("ResolveTarget() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := root.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(root.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox root 应被删除，err=%v", err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatalf("sandbox 外文件不应受影响，raw=%q err=%v", raw, err)
	}
}

func TestResolveRejectsTraversalAndRootTarget(t *testing.T) {
	root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-002")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	for _, target := range []string{
		"",
		".",
		"../outside.txt",
		`..\outside.txt`,
		"foo/../../outside.txt",
		`foo\..\..\outside.txt`,
		"<sandbox>",
		"<sandbox>/../outside.txt",
		`<sandbox>\..\outside.txt`,
		filepath.Join(string(os.PathSeparator), "absolute.txt"),
		"/absolute.txt",
		`\rooted.txt`,
		`C:\absolute.txt`,
		"C:/absolute.txt",
		`\\server\share\outside.txt`,
		"//server/share/outside.txt",
	} {
		if _, err := root.ResolveTarget(target); err == nil {
			t.Fatalf("目标 %q 必须被拒绝", target)
		}
	}
}

func TestResolveAcceptsSandboxRelativePaths(t *testing.T) {
	root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-relative")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	for _, target := range []string{
		"workspace/file.txt",
		"workspace/nested/result.json",
	} {
		resolved, err := root.Resolve(target)
		if err != nil {
			t.Fatalf("安全相对路径 %q 应可解析：%v", target, err)
		}
		expected := filepath.Join(root.Path(), filepath.FromSlash(target))
		if !samePath(resolved, expected) {
			t.Fatalf("Resolve(%q) = %q，期望 %q", target, resolved, expected)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-003")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	outside := t.TempDir()
	link := filepath.Join(root.Path(), "escape")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建测试符号链接：%v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := root.Resolve("escape/file.txt"); err == nil {
		t.Fatal("经符号链接逃逸 sandbox 的路径必须被拒绝")
	}
}

func TestCreateRejectsSymlinkedBase(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatalf("mkdir actual: %v", err)
	}
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(actual, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建测试符号链接：%v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := Create(link, "run-linked"); err == nil {
		t.Fatal("经符号链接重定向的 sandbox base 必须被拒绝")
	}
}

func TestCleanupFailsClosedWhenMarkerChanges(t *testing.T) {
	root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-004")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	raw, err := os.ReadFile(root.marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var document markerDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	document.Nonce = "tampered"
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatalf("encode marker: %v", err)
	}
	if err := os.WriteFile(root.marker, raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if err := root.Cleanup(); err == nil {
		t.Fatal("标记被篡改时 Cleanup 必须 fail closed")
	}
	if _, err := os.Stat(root.Path()); err != nil {
		t.Fatalf("清理失败时必须保留证据目录：%v", err)
	}
}

func TestValidateDangerousRootRejectsCurrentDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := validateDangerousRoot(cwd); err == nil {
		t.Fatal("当前工作目录不能作为 sandbox root")
	}
}

func TestCreateRejectsInvalidAndDuplicateRunID(t *testing.T) {
	base := filepath.Join(t.TempDir(), "evaluation")
	if _, err := Create(base, "../escape"); err == nil {
		t.Fatal("非法 runID 必须被拒绝")
	}
	root, err := Create(base, "run-duplicate")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })
	if _, err := Create(base, "run-duplicate"); err == nil {
		t.Fatal("重复 runID 必须被拒绝")
	}
}

func TestCreateRejectsDangerousOrUnusableBase(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if _, err := Create(cwd, "run-current"); err == nil {
		t.Fatal("当前工作目录不能作为 sandbox base")
	}

	volumeRoot := filepath.VolumeName(cwd) + string(os.PathSeparator)
	if _, err := Create(volumeRoot, "run-volume-root"); err == nil {
		t.Fatal("文件系统根目录不能作为 sandbox base")
	}

	baseFile := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(baseFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	if _, err := Create(baseFile, "run-file-base"); err == nil {
		t.Fatal("普通文件不能作为 sandbox base")
	}
}

func TestResolveAcceptsSafeExistingSymlink(t *testing.T) {
	root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-safe-link")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	actual := filepath.Join(root.Path(), "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatalf("mkdir actual: %v", err)
	}
	link := filepath.Join(root.Path(), "linked")
	if err := os.Symlink(actual, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建测试符号链接：%v", err)
		}
		t.Fatalf("Symlink() error = %v", err)
	}
	resolved, err := root.Resolve("linked/file.txt")
	if err != nil {
		t.Fatalf("指向 sandbox 内部的符号链接应允许：%v", err)
	}
	if filepath.Dir(resolved) != actual {
		t.Fatalf("resolved=%s actual=%s", resolved, actual)
	}
}

func TestCleanupRejectsMissingAndInvalidMarker(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-missing-marker")
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := os.Remove(root.marker); err != nil {
			t.Fatalf("remove marker: %v", err)
		}
		if err := root.Cleanup(); err == nil {
			t.Fatal("缺少 marker 时必须 fail closed")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		root, err := Create(filepath.Join(t.TempDir(), "evaluation"), "run-invalid-marker")
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := os.WriteFile(root.marker, []byte(`{"schemaVersion":`), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if err := root.Cleanup(); err == nil {
			t.Fatal("marker JSON 无效时必须 fail closed")
		}
	})

	t.Run("trailing json", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "marker.json")
		if err := os.WriteFile(path, []byte(`{"schemaVersion":"v1"} {}`), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if _, err := readMarker(path); err == nil {
			t.Fatal("marker 尾部多余 JSON 必须被拒绝")
		}
	})
}

func TestCleanupRejectsUnsafeChangedAndExternalMarkerPaths(t *testing.T) {
	t.Run("unsafe root", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd() error = %v", err)
		}
		root := &Root{path: cwd}
		if err := root.Cleanup(); err == nil {
			t.Fatal("危险 root 必须被拒绝")
		}
	})

	t.Run("missing root", func(t *testing.T) {
		root := &Root{path: filepath.Join(t.TempDir(), "missing")}
		if err := root.Cleanup(); err == nil {
			t.Fatal("不存在的 root 必须被拒绝")
		}
	})

	t.Run("redirected root", func(t *testing.T) {
		parent := t.TempDir()
		actual := filepath.Join(parent, "actual")
		if err := os.Mkdir(actual, 0o700); err != nil {
			t.Fatalf("mkdir actual: %v", err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(actual, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("当前 Windows 环境不允许创建测试符号链接：%v", err)
			}
			t.Fatalf("Symlink() error = %v", err)
		}
		root := &Root{path: link}
		if err := root.Cleanup(); err == nil {
			t.Fatal("被重定向的 root 必须被拒绝")
		}
	})

	t.Run("external marker", func(t *testing.T) {
		rootPath := filepath.Join(t.TempDir(), "run")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		externalMarker := filepath.Join(t.TempDir(), "marker.json")
		document := markerDocument{
			SchemaVersion: markerVersion,
			RunID:         "run-external-marker",
			Nonce:         "nonce",
			Root:          rootPath,
			CreatedAt:     "now",
		}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("marshal marker: %v", err)
		}
		if err := os.WriteFile(externalMarker, raw, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		root := &Root{
			path:   rootPath,
			runID:  document.RunID,
			nonce:  document.Nonce,
			marker: externalMarker,
		}
		if err := root.Cleanup(); err == nil {
			t.Fatal("root 外的 marker 必须被拒绝")
		}
	})

	t.Run("symlinked marker", func(t *testing.T) {
		rootPath := filepath.Join(t.TempDir(), "run")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		externalMarker := filepath.Join(t.TempDir(), "marker.json")
		document := markerDocument{
			SchemaVersion: markerVersion,
			RunID:         "run-symlinked-marker",
			Nonce:         "nonce",
			Root:          rootPath,
			CreatedAt:     "now",
		}
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("marshal marker: %v", err)
		}
		if err := os.WriteFile(externalMarker, raw, 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		markerLink := filepath.Join(rootPath, markerFileName)
		if err := os.Symlink(externalMarker, markerLink); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("当前 Windows 环境不允许创建测试符号链接：%v", err)
			}
			t.Fatalf("Symlink() error = %v", err)
		}
		root := &Root{
			path:   rootPath,
			runID:  document.RunID,
			nonce:  document.Nonce,
			marker: markerLink,
		}
		if err := root.Cleanup(); err == nil {
			t.Fatal("符号链接 marker 必须被拒绝")
		}
	})
}

func TestContainmentHelpers(t *testing.T) {
	root := t.TempDir()
	if err := ensureContained(root, root, true); err != nil {
		t.Fatalf("allowRoot=true 时应允许根目录：%v", err)
	}
	if err := ensureContained(root, root, false); err == nil {
		t.Fatal("allowRoot=false 时必须拒绝根目录")
	}
	if err := ensureContained(root, filepath.Join(filepath.Dir(root), "outside"), false); err == nil {
		t.Fatal("外部路径必须被拒绝")
	}
	if _, err := resolveWithMissingTail(filepath.Join(root, "a", "b", "c")); err != nil {
		t.Fatalf("缺失尾部路径应可解析：%v", err)
	}
}

func TestReadMarkerRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	raw := bytes.NewBufferString(`{"schemaVersion":"v1","runId":"run","nonce":"n","root":"r","createdAt":"now","extra":true}`)
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, err := readMarker(path); err == nil {
		t.Fatal("marker 未知字段必须被拒绝")
	}
}

func TestDangerousRootAndRepositoryDetection(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	volumeRoot := filepath.VolumeName(cwd) + string(os.PathSeparator)
	if err := validateDangerousRoot(volumeRoot); err == nil {
		t.Fatal("文件系统根目录必须被拒绝")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := validateDangerousRoot(home); err == nil {
			t.Fatal("用户目录必须被拒绝")
		}
	}
	repositoryRoot := findRepositoryRoot(cwd)
	if repositoryRoot == "" {
		t.Fatal("应从当前测试目录找到仓库根目录")
	}
	if err := validateDangerousRoot(repositoryRoot); err == nil {
		t.Fatal("仓库根目录必须被拒绝")
	}
	if root := findRepositoryRoot(t.TempDir()); root != "" {
		t.Fatalf("普通临时目录不应被识别为仓库：%s", root)
	}
}

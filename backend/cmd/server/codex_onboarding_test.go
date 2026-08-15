package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/hookassets"
)

func TestInitCodexInstallsRuntimeProjectHook(t *testing.T) {
	project := t.TempDir()
	report, err := writeProjectInitFiles(project, projectInitModeCodex)
	if err != nil {
		t.Fatalf("init codex: %v", err)
	}

	expected := []string{
		projectCodexConfigSnippetPath(project),
		projectCodexProjectSnippetPath(project),
		projectCodexProjectConfigPath(project),
		projectCodexHookAdapterPath(project),
		projectCodexHookCorePath(project),
	}
	for _, path := range expected {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated Codex file %s: %v", path, err)
		}
		if !containsPath(report.CodexFiles, path) {
			t.Fatalf("Codex report missing %s: %+v", path, report.CodexFiles)
		}
	}

	legacyPath := filepath.Join(project, ".agenttoolgate", "clients", "codex.hooks.json")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("init codex must not generate obsolete project hook JSON %s", legacyPath)
	}

	config := readTestFile(t, projectCodexProjectConfigPath(project))
	for _, want := range []string{
		"[features]",
		"hooks = true",
		"[hooks]",
		"[[hooks.PreToolUse]]",
		`matcher = "` + localActionHookMatcher + `"`,
		"[[hooks.PreToolUse.hooks]]",
		`type = "command"`,
		"command = '" + codexUnixHookCommand + "'",
		"commandWindows = '" + currentCodexWindowsHookCommand() + "'",
		"timeout = 30",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex project config missing %q:\n%s", want, config)
		}
	}
	if snippet := readTestFile(t, projectCodexProjectSnippetPath(project)); snippet != config {
		t.Fatalf("project Hook merge snippet differs from generated config")
	}
	for _, forbidden := range []string{"[projects.", "trusted_hash", "dangerously-bypass-hook-trust"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("project config must not persist user trust value %q:\n%s", forbidden, config)
		}
	}

	adapter := readTestFile(t, projectCodexHookAdapterPath(project))
	if !strings.Contains(adapter, "from _guard_core import") {
		t.Fatalf("installed Codex adapter must import its local guard core")
	}
	core := readTestFile(t, projectCodexHookCorePath(project))
	if strings.Contains(core, "cannot load shared guard core from") ||
		strings.Contains(core, `Path(__file__).parents[2] / ".claude" / "hooks"`) {
		t.Fatalf("installed Codex core must not depend on a sibling Claude directory")
	}
	if !strings.Contains(core, "def local_guard_preview") {
		t.Fatalf("installed Codex core is missing offline guard implementation")
	}
}

func TestInitCodexDoesNotOverwriteRuntimeFiles(t *testing.T) {
	project := t.TempDir()
	customConfig := []byte("# user project config\n")
	customAdapter := []byte("# user hook adapter\n")
	for path, content := range map[string][]byte{
		projectCodexProjectConfigPath(project): customConfig,
		projectCodexHookAdapterPath(project):   customAdapter,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create parent for %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write existing file %s: %v", path, err)
		}
	}

	report, err := writeProjectInitFiles(project, projectInitModeCodex)
	if err != nil {
		t.Fatalf("init codex: %v", err)
	}
	for path, want := range map[string][]byte{
		projectCodexProjectConfigPath(project): customConfig,
		projectCodexHookAdapterPath(project):   customAdapter,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read existing file %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("init overwrote existing file %s\nwant=%s\ngot=%s", path, want, got)
		}
		if !containsPath(report.Skipped, path) {
			t.Fatalf("init report must mark existing file skipped: %s", path)
		}
	}
	if _, err := os.Stat(projectCodexHookCorePath(project)); err != nil {
		t.Fatalf("init should still create missing self-contained core: %v", err)
	}
}

func TestInitCodexRejectsExistingHooksJSONBeforeWriting(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	excludePath := testGitExcludePath(t, project)
	excludeBefore := readTestFile(t, excludePath)
	hooksPath := projectCodexHooksJSONPath(project)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code == 0 {
		t.Fatalf("init should reject competing hooks.json: %s", stdout.String())
	}
	for _, want := range []string{"hooks.json", "保留一种来源"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("conflict error missing %q: %s", want, stderr.String())
		}
	}
	for _, path := range []string{
		projectConfigPath(project),
		projectProtectedPath(project),
		projectReadmePath(project),
		projectPromptPath(project),
		projectCodexConfigSnippetPath(project),
		projectCodexProjectSnippetPath(project),
		projectCodexProjectConfigPath(project),
		projectCodexHookAdapterPath(project),
		projectCodexHookCorePath(project),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("conflicting init must not write %s: %v", path, err)
		}
	}
	if excludeAfter := readTestFile(t, excludePath); excludeAfter != excludeBefore {
		t.Fatalf("conflicting init must not update Git local exclude:\nbefore=%s\nafter=%s", excludeBefore, excludeAfter)
	}
}

func TestInitCodexRejectsNonGitProjectBeforeWriting(t *testing.T) {
	project := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code != 2 {
		t.Fatalf("non-Git init returned %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "Git 仓库根目录") {
		t.Fatalf("non-Git error is not actionable: %s", stderr.String())
	}
	for _, path := range []string{filepath.Join(project, ".agenttoolgate"), filepath.Join(project, ".codex")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected init must not write %s: %v", path, err)
		}
	}
}

func TestInitCodexRejectsNestedDirectoryInsideAnotherGitRepository(t *testing.T) {
	outer := t.TempDir()
	initTestGitRepository(t, outer)
	project := filepath.Join(outer, "nested")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("create nested project: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code != 2 {
		t.Fatalf("nested init returned %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "目标 Git 仓库根目录") {
		t.Fatalf("nested Git error is not actionable: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("nested rejected init must not write project files: %v", err)
	}
}

func TestInitCodexAcceptsLinkedGitWorktreeRoot(t *testing.T) {
	repository := t.TempDir()
	initTestGitRepository(t, repository)
	runTestGit(t, repository, "config", "user.name", "AgentToolGate Test")
	runTestGit(t, repository, "config", "user.email", "agenttoolgate@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test repository\n"), 0o600); err != nil {
		t.Fatalf("write initial repository file: %v", err)
	}
	runTestGit(t, repository, "add", "README.md")
	runTestGit(t, repository, "commit", "--quiet", "-m", "initial")

	worktree := filepath.Join(t.TempDir(), "linked-worktree")
	runTestGit(t, repository, "worktree", "add", "--quiet", "--detach", worktree)
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree).Run()
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", worktree}, &stdout, &stderr); code != 0 {
		t.Fatalf("linked worktree init returned %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if info, err := os.Stat(filepath.Join(worktree, ".git")); err != nil || info.IsDir() {
		t.Fatalf("test setup must use a linked-worktree .git file: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(projectCodexHookAdapterPath(worktree)); err != nil {
		t.Fatalf("linked worktree init did not install Codex adapter: %v", err)
	}
	assertProjectRuntimeIgnored(t, worktree)
}

func TestInitCodexAddsProjectRuntimeToLocalGitExclude(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	excludePath := testGitExcludePath(t, project)
	original := "# 用户本地规则\n/local-only.txt\n"
	if err := os.WriteFile(excludePath, []byte(original), 0o600); err != nil {
		t.Fatalf("write existing exclude: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("init codex returned %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	raw := readTestFile(t, excludePath)
	if !strings.Contains(raw, original) || strings.Count(raw, projectRuntimeExclude) != 1 {
		t.Fatalf("local exclude did not preserve content or add one ATG rule:\n%s", raw)
	}
	if err := writeProjectHookControl(project, projectHookModeDryRun); err != nil {
		t.Fatalf("write hook control: %v", err)
	}
	if err := ensureProjectRuntimeGitExclude(project); err != nil {
		t.Fatalf("repeat exclude update: %v", err)
	}
	if raw = readTestFile(t, excludePath); strings.Count(raw, projectRuntimeExclude) != 1 {
		t.Fatalf("local exclude rule is not idempotent:\n%s", raw)
	}
	assertProjectRuntimeIgnored(t, project)
}

func TestProjectRuntimeGitExcludeRejectsSymlinkTarget(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	excludePath := testGitExcludePath(t, project)
	outside := filepath.Join(t.TempDir(), "outside-exclude")
	original := []byte("outside must stay unchanged\n")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatalf("write outside exclude: %v", err)
	}
	if err := os.Remove(excludePath); err != nil {
		t.Fatalf("remove original exclude: %v", err)
	}
	if err := os.Symlink(outside, excludePath); err != nil {
		t.Skipf("当前平台无法创建文件符号链接: %v", err)
	}
	resolvedExclude, err := projectGitExcludePath(project)
	if err != nil {
		t.Fatalf("locate exclude after symlink setup: %v", err)
	}
	if !sameHookPath(resolvedExclude, excludePath) {
		t.Fatalf("exclude path drifted: got=%s want=%s", resolvedExclude, excludePath)
	}
	info, err := os.Lstat(excludePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("test setup did not create an exclude symlink: info=%v err=%v", info, err)
	}

	if err := ensureProjectRuntimeGitExclude(project); err == nil {
		t.Fatal("local exclude update should reject symlink target")
	}
	got, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("rejected exclude update changed outside file: err=%v content=%q", err, got)
	}
}

func TestInitCodexRefreshHooksReplacesOnlyRuntimeFiles(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	customConfig := []byte("# keep user project config\n")
	if err := os.WriteFile(projectCodexProjectConfigPath(project), customConfig, 0o600); err != nil {
		t.Fatalf("write custom project config: %v", err)
	}
	for _, path := range []string{projectCodexHookAdapterPath(project), projectCodexHookCorePath(project)} {
		if err := os.WriteFile(path, []byte("# old generated hook\n"), 0o600); err != nil {
			t.Fatalf("write old hook %s: %v", path, err)
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("refresh hooks returned %d stderr=%s", code, stderr.String())
	}
	bundle := hookassets.Codex()
	for path, want := range map[string][]byte{
		projectCodexHookAdapterPath(project): bundle.Adapter,
		projectCodexHookCorePath(project):    bundle.Core,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read refreshed hook %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("hook was not refreshed: %s", path)
		}
		if !strings.Contains(stdout.String(), path) {
			t.Fatalf("refresh output missing path %s:\n%s", path, stdout.String())
		}
	}
	if got, err := os.ReadFile(projectCodexProjectConfigPath(project)); err != nil || !bytes.Equal(got, customConfig) {
		t.Fatalf("refresh must preserve project config: %v content=%s", err, got)
	}
	if !strings.Contains(stdout.String(), "已刷新") {
		t.Fatalf("refresh output missing refreshed section:\n%s", stdout.String())
	}
}

func TestInitCodexRefreshHooksSupportsHooksJSONWithoutCreatingTOML(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	hooksPath := projectCodexHooksJSONPath(project)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("refresh hooks returned %d stderr=%s", code, stderr.String())
	}
	for _, path := range []string{projectCodexHookAdapterPath(project), projectCodexHookCorePath(project)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("refresh should create missing runtime file %s: %v", path, err)
		}
	}
	for _, path := range []string{
		projectConfigPath(project),
		projectProtectedPath(project),
		projectReadmePath(project),
		projectPromptPath(project),
		projectCodexProjectConfigPath(project),
		projectCodexConfigSnippetPath(project),
		projectCodexProjectSnippetPath(project),
		projectClaudeMCPPath(project),
		projectClaudeSettingsPath(project),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refresh must not create non-runtime file %s: %v", path, err)
		}
	}
}

func TestInitCodexRefreshPreflightPreventsPartialBundleUpdate(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	adapterPath := projectCodexHookAdapterPath(project)
	oldAdapter := []byte("# old adapter\n")
	if err := os.WriteFile(adapterPath, oldAdapter, 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	if err := os.Remove(corePath); err != nil {
		t.Fatalf("remove core: %v", err)
	}
	if err := os.Mkdir(corePath, 0o700); err != nil {
		t.Fatalf("replace core with directory: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code == 0 {
		t.Fatalf("refresh should reject non-file core: %s", stdout.String())
	}
	got, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatalf("read adapter after failed refresh: %v", err)
	}
	if !bytes.Equal(got, oldAdapter) {
		t.Fatalf("failed bundle preflight must not refresh adapter")
	}
}

func TestInitCodexRefreshPreflightChecksAdapterBeforeUpdatingCore(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	oldCore := []byte("# old core\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	adapterPath := projectCodexHookAdapterPath(project)
	if err := os.Remove(adapterPath); err != nil {
		t.Fatalf("remove adapter: %v", err)
	}
	if err := os.Mkdir(adapterPath, 0o700); err != nil {
		t.Fatalf("replace adapter with directory: %v", err)
	}

	if err := writeCodexRuntimeFiles(project, &projectInitReport{}, true); err == nil {
		t.Fatal("refresh should reject non-file adapter")
	}
	got, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatalf("read core after failed preflight: %v", err)
	}
	if !bytes.Equal(got, oldCore) {
		t.Fatal("adapter preflight failure must not refresh core")
	}
}

func TestInitCodexRefreshRollsBackFirstFileWhenSecondWriteFails(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	adapterPath := projectCodexHookAdapterPath(project)
	oldCore := []byte("# old core\n")
	oldAdapter := []byte("# old adapter\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	if err := os.WriteFile(adapterPath, oldAdapter, 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}

	originalReplace := replaceCodexRuntimeFile
	replaceCodexRuntimeFile = func(root, path string, data []byte, perm os.FileMode, snapshot codexRuntimeFileSnapshot) (string, codexRuntimeFileSnapshot, error) {
		if path == adapterPath {
			return "", codexRuntimeFileSnapshot{}, fmt.Errorf("simulated adapter replacement failure")
		}
		return replaceCodexRuntimeFileFromSnapshot(root, path, data, perm, snapshot)
	}
	t.Cleanup(func() { replaceCodexRuntimeFile = originalReplace })

	if err := writeCodexRuntimeFiles(project, &projectInitReport{}, true); err == nil {
		t.Fatal("refresh should report the second-file failure")
	}
	for path, want := range map[string][]byte{corePath: oldCore, adapterPath: oldAdapter} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read rolled back file %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("failed refresh left mismatched bundle file %s", path)
		}
	}
}

func TestInitCodexRefreshPreservesConcurrentModification(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	adapterPath := projectCodexHookAdapterPath(project)
	oldCore := []byte("# old core\n")
	oldAdapter := []byte("# old adapter\n")
	concurrentAdapter := []byte("# concurrent adapter edit\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	if err := os.WriteFile(adapterPath, oldAdapter, 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}

	originalReplace := replaceCodexRuntimeFile
	replaceCodexRuntimeFile = func(root, path string, data []byte, perm os.FileMode, snapshot codexRuntimeFileSnapshot) (string, codexRuntimeFileSnapshot, error) {
		backup, installed, err := replaceCodexRuntimeFileFromSnapshot(root, path, data, perm, snapshot)
		if err != nil {
			return "", codexRuntimeFileSnapshot{}, err
		}
		if path == corePath {
			if err := os.WriteFile(adapterPath, concurrentAdapter, 0o600); err != nil {
				return "", codexRuntimeFileSnapshot{}, err
			}
		}
		return backup, installed, nil
	}
	t.Cleanup(func() { replaceCodexRuntimeFile = originalReplace })

	err := writeCodexRuntimeFiles(project, &projectInitReport{}, true)
	if err == nil || !strings.Contains(err.Error(), "并发修改") {
		t.Fatalf("refresh should reject concurrent modification, got %v", err)
	}
	if got := readTestFile(t, corePath); got != string(oldCore) {
		t.Fatalf("failed refresh must roll core back, got %q", got)
	}
	if got := readTestFile(t, adapterPath); got != string(concurrentAdapter) {
		t.Fatalf("failed refresh must preserve concurrent adapter edit, got %q", got)
	}
}

func TestInitCodexRefreshRestoresModificationAtAtomicReplace(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	adapterPath := projectCodexHookAdapterPath(project)
	oldCore := []byte("# old core\n")
	oldAdapter := []byte("# old adapter\n")
	concurrentCore := []byte("# concurrent core edit\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	if err := os.WriteFile(adapterPath, oldAdapter, 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}

	originalReplace := replaceProjectFileWithBackup
	injected := false
	replaceProjectFileWithBackup = func(targetPath, replacementPath string) (string, error) {
		if targetPath == corePath && !injected {
			injected = true
			if err := os.WriteFile(targetPath, concurrentCore, 0o600); err != nil {
				return "", err
			}
		}
		return originalReplace(targetPath, replacementPath)
	}
	t.Cleanup(func() { replaceProjectFileWithBackup = originalReplace })

	err := writeCodexRuntimeFiles(project, &projectInitReport{}, true)
	if err == nil || !strings.Contains(err.Error(), "并发修改") {
		t.Fatalf("refresh should reject replace-window modification, got %v", err)
	}
	if got := readTestFile(t, corePath); got != string(concurrentCore) {
		t.Fatalf("replace-window edit must remain at the target, got %q", got)
	}
	if got := readTestFile(t, adapterPath); got != string(oldAdapter) {
		t.Fatalf("failed first-file refresh must not touch adapter, got %q", got)
	}
}

func TestCodexRollbackRestoresConcurrentTargetAndPreservesOriginalBackup(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	oldCore := []byte("# old core\n")
	concurrentCore := []byte("# concurrent rollback edit\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	previous, err := snapshotCodexRuntimeFile(corePath)
	if err != nil {
		t.Fatalf("snapshot old core: %v", err)
	}
	bundle := hookassets.Codex()
	backupPath, installed, err := replaceCodexRuntimeFileFromSnapshot(project, corePath, bundle.Core, 0o600, previous)
	if err != nil {
		t.Fatalf("install core with backup: %v", err)
	}

	originalReplace := replaceProjectFileWithBackup
	injected := false
	replaceProjectFileWithBackup = func(targetPath, replacementPath string) (string, error) {
		if targetPath == corePath && !injected {
			injected = true
			if err := os.WriteFile(targetPath, concurrentCore, 0o600); err != nil {
				return "", err
			}
		}
		return originalReplace(targetPath, replacementPath)
	}
	t.Cleanup(func() { replaceProjectFileWithBackup = originalReplace })

	err = restoreCodexRuntimeBackup(project, corePath, backupPath, installed)
	if err == nil || !strings.Contains(err.Error(), "并发版本已换回") {
		t.Fatalf("rollback should report the preserved conflict, got %v", err)
	}
	if got := readTestFile(t, corePath); got != string(concurrentCore) {
		t.Fatalf("concurrent rollback edit must remain at the target, got %q", got)
	}
	recoveryDir := filepath.Join(project, ".tmp", "agenttoolgate", "recovery")
	if !directoryContainsFileContent(t, recoveryDir, oldCore, corePath) {
		t.Fatal("rollback conflict must preserve the original core in a backup file")
	}
}

func TestRollbackCodexRuntimeFilesAcceptsAlreadyRemovedCreatedFile(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".codex", "hooks", "created.py")
	outcomes := []codexRuntimeFileOutcome{{
		spec:    codexRuntimeFileSpec{path: path, content: []byte("generated\n")},
		created: true,
	}}
	if err := rollbackCodexRuntimeFiles(project, outcomes); err != nil {
		t.Fatalf("already removed created file is already rolled back: %v", err)
	}
}

func TestHookControlWriteRejectsExternalTmpLink(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(project, ".tmp")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Skipf("当前平台无法创建目录 junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前平台无法创建目录符号链接: %v", err)
	}

	if err := writeProjectHookControl(project, projectHookModeLive); err == nil {
		t.Fatal("hook control write should reject external .tmp link")
	}
	outsideControl := filepath.Join(outside, "agenttoolgate", "hook-control.json")
	if _, err := os.Stat(outsideControl); !os.IsNotExist(err) {
		t.Fatalf("hook control escaped project root: %v", err)
	}
}

func TestProjectWritesRejectDirectFileSymlinks(t *testing.T) {
	tests := []struct {
		name   string
		target func(string) string
		write  func(string) error
	}{
		{
			name:   "Codex config",
			target: projectCodexProjectConfigPath,
			write: func(project string) error {
				_, err := writeProjectInitFiles(project, projectInitModeCodex)
				return err
			},
		},
		{
			name:   "Codex core",
			target: projectCodexHookCorePath,
			write: func(project string) error {
				return writeCodexRuntimeFiles(project, &projectInitReport{}, true)
			},
		},
		{
			name:   "Codex adapter",
			target: projectCodexHookAdapterPath,
			write: func(project string) error {
				return writeCodexRuntimeFiles(project, &projectInitReport{}, true)
			},
		},
		{
			name:   "hook control",
			target: projectHookControlPath,
			write: func(project string) error {
				return writeProjectHookControl(project, projectHookModeLive)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside.txt")
			original := []byte("outside must stay unchanged\n")
			if err := os.WriteFile(outside, original, 0o600); err != nil {
				t.Fatalf("write outside file: %v", err)
			}
			target := test.target(project)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatalf("create target parent: %v", err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("当前平台无法创建文件符号链接: %v", err)
			}
			if err := test.write(project); err == nil {
				t.Fatal("project write should reject direct file symlink")
			}
			got, err := os.ReadFile(outside)
			if err != nil {
				t.Fatalf("read outside file: %v", err)
			}
			if !bytes.Equal(got, original) {
				t.Fatal("rejected symlink write changed outside file")
			}
		})
	}
}

func TestWriteFileIfMissingFallsBackWhenHardLinksAreUnavailable(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(project, ".agenttoolgate", "fallback.txt")
	originalLink := linkProjectFile
	linkProjectFile = func(_, _ string) error { return fmt.Errorf("hard links unavailable") }
	t.Cleanup(func() { linkProjectFile = originalLink })

	created, err := writeFileIfMissing(project, target, []byte("complete content\n"), 0o600)
	if err != nil {
		t.Fatalf("exclusive fallback write: %v", err)
	}
	if !created {
		t.Fatal("fallback should create the missing file")
	}
	if got := readTestFile(t, target); got != "complete content\n" {
		t.Fatalf("fallback wrote unexpected content: %q", got)
	}
}

func TestWriteFileIfMissingFallbackDoesNotOverwriteConcurrentFile(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(project, ".agenttoolgate", "fallback-race.txt")
	concurrent := []byte("concurrent content\n")
	originalLink := linkProjectFile
	originalRename := renameProjectFileNoReplace
	linkProjectFile = func(_, _ string) error { return fmt.Errorf("hard links unavailable") }
	renameProjectFileNoReplace = func(oldPath, newPath string) error {
		if err := os.WriteFile(newPath, concurrent, 0o600); err != nil {
			return err
		}
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() {
		linkProjectFile = originalLink
		renameProjectFileNoReplace = originalRename
	})

	created, err := writeFileIfMissing(project, target, []byte("generated content\n"), 0o600)
	if err != nil {
		t.Fatalf("exclusive fallback write: %v", err)
	}
	if created {
		t.Fatal("fallback must not replace a concurrently created file")
	}
	if got := readTestFile(t, target); got != string(concurrent) {
		t.Fatalf("fallback overwrote concurrent content: %q", got)
	}
}

func TestInitClaudeRejectsCodexHookRefresh(t *testing.T) {
	project := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "claude", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 2 {
		t.Fatalf("init claude refresh returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "仅适用于 init codex") {
		t.Fatalf("unexpected error: %s", stderr.String())
	}
	if _, err := os.Stat(projectConfigPath(project)); !os.IsNotExist(err) {
		t.Fatalf("invalid refresh must not write project files: %v", err)
	}
}

func TestProjectCodexDiagnosticsSeparateInstallationAndTrust(t *testing.T) {
	project := t.TempDir()

	missing := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex 项目配置: missing",
		"Codex hooks.json: missing",
		"Codex Hook adapter: missing",
		"Codex Hook core: missing",
	} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing diagnostics should contain %q:\n%s", want, missing)
		}
	}

	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	configured := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex 项目配置: configured",
		"Codex Hook adapter: current",
		"Codex Hook core: current",
		"Codex Git:",
		"Codex Python 3:",
		"ATG Hook mode: off",
		"ATG Hook endpoint: missing",
		"Codex 项目信任: 需在用户 config.toml 中显式确认",
		"Codex Hook 信任: 需在 Codex /hooks 中确认 trusted",
	} {
		if !strings.Contains(configured, want) {
			t.Fatalf("configured diagnostics should contain %q:\n%s", want, configured)
		}
	}

	if err := os.WriteFile(projectCodexHookAdapterPath(project), []byte("# modified\n"), 0o600); err != nil {
		t.Fatalf("modify adapter: %v", err)
	}
	modified := formatProjectCodexDiagnostics(project)
	if !strings.Contains(modified, "Codex Hook adapter: modified") {
		t.Fatalf("modified adapter must be visible:\n%s", modified)
	}
}

func TestProjectCodexDiagnosticsWarnsAboutCompetingHookSources(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	if err := os.WriteFile(projectCodexHooksJSONPath(project), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	diagnostics := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex hooks.json: present",
		"config.toml 与 hooks.json 同层并存",
		"请人工保留一种 Hook 来源",
	} {
		if !strings.Contains(diagnostics, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, diagnostics)
		}
	}
}

func TestCodexProjectConfigStatusRejectsInactiveOrMisleadingContent(t *testing.T) {
	valid := renderCodexProjectHookConfig()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "generated", body: valid, want: "configured"},
		{name: "comment only", body: "# [[hooks.PreToolUse]]\n# .codex/hooks/agent-guard-pretool.py\n", want: "custom"},
		{name: "disabled", body: strings.Replace(valid, "hooks = true", "hooks = false", 1), want: "custom"},
		{name: "dotted disabled", body: strings.Replace(valid, "[features]\nhooks = true", "features.hooks = false", 1), want: "custom"},
		{name: "wrong matcher", body: strings.Replace(valid, localActionHookMatcher, "^Read$", 1), want: "custom"},
		{name: "wrong command", body: strings.Replace(valid, codexUnixHookCommand, "python3 other.py", 1), want: "custom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if got := codexProjectConfigStatus(path); got != tc.want {
				t.Fatalf("status=%s want=%s\n%s", got, tc.want, tc.body)
			}
		})
	}
}

func TestStaticCodexHookExamplesUseSharedMatcher(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, path := range []string{
		filepath.Join(repoRoot, "examples", "client-configs", "codex.hooks.json"),
		filepath.Join(repoRoot, "examples", "client-configs", "codex.project-hook.snippet.toml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read static Codex example %s: %v", path, err)
		}
		if !strings.Contains(string(raw), localActionHookMatcher) {
			t.Fatalf("static Codex example %s does not use the shared matcher", path)
		}
	}
}

func TestInitCodexExistingConfigLeavesActionableMergeSnippet(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
	configPath := projectCodexProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("# custom config\n"), 0o600); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Codex 接入待处理") ||
		!strings.Contains(stdout.String(), "codex.project-hook.snippet.toml") {
		t.Fatalf("init output lacks merge guidance:\n%s", stdout.String())
	}
	if _, err := os.Stat(projectCodexProjectSnippetPath(project)); err != nil {
		t.Fatalf("merge snippet missing: %v", err)
	}
}

func TestProjectHookActivationPublishesRuntimeEndpoint(t *testing.T) {
	root := t.TempDir()
	path := projectHookControlPath(root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	activation := projectHookControlActivation{
		root:       root,
		path:       path,
		mode:       projectHookModeLive,
		endpoint:   "http://127.0.0.1:8090",
		executable: executable,
	}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish control: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	var doc hookControlDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	if doc.Endpoint != "http://127.0.0.1:8090" {
		t.Fatalf("endpoint=%q", doc.Endpoint)
	}
	if doc.Executable == "" {
		t.Fatal("runtime executable was not published")
	}
}

func TestProjectHookActivationUsesConfiguredNonDefaultPort(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	cfg := config.Config{ProjectRoot: project, Host: "127.0.0.1", Port: "18091"}
	activation, err := newProjectHookControlActivation(cfg, projectHookControlPath(project), projectHookModeLive)
	if err != nil {
		t.Fatalf("build activation: %v", err)
	}
	if activation.endpoint != "http://127.0.0.1:18091" {
		t.Fatalf("runtime endpoint=%q", activation.endpoint)
	}
	if activation.executable == "" {
		t.Fatal("current adapter activation must publish executable")
	}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish activation: %v", err)
	}
	doc, err := readHookControlDocument(project)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if doc.Endpoint != activation.endpoint {
		t.Fatalf("control endpoint=%q want=%q", doc.Endpoint, activation.endpoint)
	}
}

func TestProjectHookActivationKeepsLegacyControlShapeForModifiedAdapter(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	if err := os.WriteFile(projectCodexHookAdapterPath(project), []byte("# legacy adapter\n"), 0o600); err != nil {
		t.Fatalf("write legacy adapter: %v", err)
	}
	cfg := config.Config{ProjectRoot: project, Host: "127.0.0.1", Port: "18092"}
	activation, err := newProjectHookControlActivation(cfg, projectHookControlPath(project), projectHookModeLive)
	if err != nil {
		t.Fatalf("build activation: %v", err)
	}
	if activation.endpoint != "" || activation.executable != "" {
		t.Fatalf("modified adapter received unsupported runtime metadata: %+v", activation)
	}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish activation: %v", err)
	}
	raw, err := os.ReadFile(projectHookControlPath(project))
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	for field := range fields {
		if field != "mode" && field != "updatedAt" && field != "reason" {
			t.Fatalf("legacy control contains unsupported field %q: %s", field, raw)
		}
	}
}

func TestHookControlEndpointOnlyAcceptsLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:8090",
		"http://example.com:8090",
		"http://127.0.0.1:8090/path",
		"http://user@127.0.0.1:8090",
		"http://127.0.0.1",
	} {
		if _, err := normalizeHookControlEndpoint(endpoint); err == nil {
			t.Fatalf("endpoint should be rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:8090",
		"http://localhost:8090/",
		"http://[::1]:8090",
	} {
		if _, err := normalizeHookControlEndpoint(endpoint); err != nil {
			t.Fatalf("endpoint should be accepted: %s: %v", endpoint, err)
		}
	}
}

func TestHookControlExecutableRequiresExistingAbsoluteFile(t *testing.T) {
	for _, executable := range []string{"relative.exe", filepath.Join(t.TempDir(), "missing.exe")} {
		if _, err := normalizeHookControlExecutable(executable); err == nil {
			t.Fatalf("executable should be rejected: %s", executable)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	if _, err := normalizeHookControlExecutable(executable); err != nil {
		t.Fatalf("current executable should be accepted: %v", err)
	}
}

func TestCodexPythonStatusRejectsExecutableThatIsNotPython3(t *testing.T) {
	dir := t.TempDir()
	name := "python3"
	if runtime.GOOS == "windows" {
		name = "python.exe"
	}
	fakePython := filepath.Join(dir, name)
	current, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	raw, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}
	if err := os.WriteFile(fakePython, raw, 0o700); err != nil {
		t.Fatalf("write fake Python executable: %v", err)
	}
	t.Setenv("PATH", dir)
	if status := codexPythonStatus(); !strings.HasPrefix(status, "unusable") {
		t.Fatalf("non-Python executable must not be reported available: %s", status)
	}
}

func TestCodexPythonCandidatesIncludeWindowsLauncherFallback(t *testing.T) {
	candidates := codexPythonCandidates("windows")
	if len(candidates) != 2 {
		t.Fatalf("unexpected Windows Python candidates: %+v", candidates)
	}
	if candidates[0].command != "python" || candidates[0].display != "python" {
		t.Fatalf("python must remain the first Windows candidate: %+v", candidates[0])
	}
	if candidates[1].command != "py" || len(candidates[1].args) != 1 ||
		candidates[1].args[0] != "-3" || candidates[1].display != "py -3" {
		t.Fatalf("py -3 fallback is missing: %+v", candidates[1])
	}
	for _, command := range []string{codexWinHookCommand, codexWinPyHookCommand} {
		if !codexWindowsHookCommandSupported(command) {
			t.Fatalf("generated config must accept Windows command %q", command)
		}
	}
	if got := selectCodexWindowsHookCommand(codexPythonInvocation{command: "py"}, nil); got != codexWinPyHookCommand {
		t.Fatalf("py launcher must render the py -3 hook command, got %q", got)
	}
	if got := selectCodexWindowsHookCommand(codexPythonInvocation{command: "python"}, nil); got != codexWinHookCommand {
		t.Fatalf("python launcher must render the python hook command, got %q", got)
	}
	if got := selectCodexWindowsHookCommand(codexPythonInvocation{}, fmt.Errorf("missing")); got != codexWinHookCommand {
		t.Fatalf("unavailable Python probe must keep the portable fallback, got %q", got)
	}
}

func TestInitRejectsProjectDirectorySymlink(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(project, ".codex")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Skipf("当前平台无法创建目录 junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前平台无法创建目录符号链接: %v", err)
	}

	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err == nil {
		t.Fatal("init should reject .codex symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote outside project through symlink: %v", err)
	}
}

func TestRunDoctorIncludesProjectCodexDiagnostics(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"doctor", "--dir", project}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Codex 项目接入诊断") ||
		!strings.Contains(output, "Codex Hook adapter: current") ||
		!strings.Contains(output, "ATG 不会自动写入或信任用户级 Codex 配置") {
		t.Fatalf("doctor output missing Codex onboarding diagnostics:\n%s", output)
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func directoryContainsFileContent(t *testing.T, dir string, expected []byte, excludedPath string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory %s: %v", dir, err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() || sameHookPath(path, excludedPath) {
			continue
		}
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(content, expected) {
			return true
		}
	}
	return false
}

func backupContaining(t *testing.T, paths []string, expected []byte) string {
	t.Helper()
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(content, expected) {
			return path
		}
	}
	t.Fatalf("no recovery backup contains %q: %v", expected, paths)
	return ""
}

func initTestGitRepository(t *testing.T, path string) {
	t.Helper()
	if output, err := exec.Command("git", "init", "--quiet", path).CombinedOutput(); err != nil {
		t.Fatalf("init test Git repository: %v: %s", err, output)
	}
}

func runTestGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func testGitExcludePath(t *testing.T, repository string) string {
	t.Helper()
	command := exec.Command("git", "-C", repository, "rev-parse", "--git-common-dir")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("locate Git local exclude: %v: %s", err, output)
	}
	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repository, gitDir)
	}
	return filepath.Clean(filepath.Join(gitDir, "info", "exclude"))
}

func assertProjectRuntimeIgnored(t *testing.T, repository string) {
	t.Helper()
	control := projectHookControlPath(repository)
	if err := os.MkdirAll(filepath.Dir(control), 0o700); err != nil {
		t.Fatalf("create project runtime: %v", err)
	}
	if err := os.WriteFile(control, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write project runtime fixture: %v", err)
	}
	command := exec.Command("git", "-C", repository, "status", "--porcelain", "--untracked-files=all")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	if strings.Contains(string(output), ".tmp/agenttoolgate/") {
		t.Fatalf("project runtime leaked into git status:\n%s", output)
	}
}

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareProjectUpUsesResolvedDirectoryAsTrustedRoot(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("init project: %v", err)
	}

	projectConfig := defaultProjectRunConfig(project)
	projectConfig.ProjectRoot = t.TempDir()
	raw, err := json.Marshal(projectConfig)
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}
	if err := os.WriteFile(projectConfigPath(project), append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	cfg, _, _, _, _, err := prepareProjectUp(commandOptions{Command: "up", Dir: project})
	if err != nil {
		t.Fatalf("prepare project up: %v", err)
	}
	expected, err := resolveProjectRoot(project)
	if err != nil {
		t.Fatalf("resolve project path: %v", err)
	}
	if cfg.ProjectRoot != expected {
		t.Fatalf("trusted root must follow resolved --dir, got %q want %q", cfg.ProjectRoot, expected)
	}
}

func TestPrepareProjectUpRejectsInvalidProjectProtectionBeforeLiveControl(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("init project: %v", err)
	}
	projectConfig := defaultProjectRunConfig(project)
	projectConfig.HookMode = projectHookModeLive
	rawConfig, err := json.Marshal(projectConfig)
	if err != nil {
		t.Fatalf("marshal project config: %v", err)
	}
	if err := os.WriteFile(projectConfigPath(project), append(rawConfig, '\n'), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if err := os.WriteFile(projectProtectedPath(project), []byte(`{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}`), 0o600); err != nil {
		t.Fatalf("write invalid protected config: %v", err)
	}

	if _, _, _, _, _, err := prepareProjectUp(commandOptions{Command: "up", Dir: project}); err == nil {
		t.Fatal("invalid project protection must reject up")
	} else if _, statErr := os.Stat(projectHookControlPath(project)); !os.IsNotExist(statErr) {
		t.Fatalf("invalid config must not leave live hook control, got %v", statErr)
	}
}

func TestPrepareProjectUpRejectsInvalidHookMode(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown", raw: `{"hookMode":"preview"}`},
		{name: "missing", raw: `{"host":"127.0.0.1"}`},
		{name: "null", raw: `{"hookMode":null}`},
		{name: "duplicate", raw: `{"hookMode":"live","hookMode":"off"}`},
		{name: "case alias duplicate", raw: `{"hookMode":"live","HookMode":"off"}`},
		{name: "duplicate nested", raw: `{"hookMode":"live","workspace":{"name":"first","name":"second"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
				t.Fatalf("init project: %v", err)
			}
			if err := os.WriteFile(projectConfigPath(project), []byte(tc.raw), 0o600); err != nil {
				t.Fatalf("write project config: %v", err)
			}

			if _, _, _, _, _, err := prepareProjectUp(commandOptions{Command: "up", Dir: project}); err == nil {
				t.Fatal("invalid hook mode must reject up")
			}
		})
	}
}

func TestPrepareProjectUpRejectsInvalidProjectConfigDocuments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unknown root field",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false,"unexpected":true}`,
		},
		{
			name: "wrong case root field",
			raw:  `{"Host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "unknown workspace field",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org","unexpected":true},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "wrong case workspace field",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"Name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "trailing json",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false} {}`,
		},
		{
			name: "wrong port type",
			raw:  `{"host":"127.0.0.1","port":"8080","workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "zero port",
			raw:  `{"host":"127.0.0.1","port":0,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "port above range",
			raw:  `{"host":"127.0.0.1","port":65536,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "missing open browser",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run"}`,
		},
		{
			name: "null open browser",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":null}`,
		},
		{
			name: "null project root",
			raw:  `{"projectRoot":null,"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "empty project root",
			raw:  `{"projectRoot":"","host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "blank workspace slug",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":" ","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`,
		},
		{
			name: "normalized hook mode is not silently accepted",
			raw:  `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":" LIVE ","openBrowser":false}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
				t.Fatalf("create project config dir: %v", err)
			}
			if err := os.WriteFile(projectConfigPath(project), []byte(tc.raw), 0o600); err != nil {
				t.Fatalf("write project config: %v", err)
			}

			if _, _, _, _, _, err := prepareProjectUp(commandOptions{Command: "up", Dir: project}); err == nil {
				t.Fatal("invalid project config must reject up")
			}
		})
	}
}

func TestLoadProjectRunConfigRejectsLinkedConfigDirectory(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	cfg := defaultProjectRunConfig(project)
	if err := os.WriteFile(filepath.Join(outside, "config.json"), []byte(renderProjectConfigFile(cfg)), 0o600); err != nil {
		t.Fatalf("write outside config: %v", err)
	}
	createProjectConfigDirectoryLink(t, filepath.Join(project, ".agenttoolgate"), outside)

	_, _, _, err := loadProjectRunConfig(project)
	if err == nil || !strings.Contains(err.Error(), "路径不可信") {
		t.Fatalf("linked config directory must be rejected, got %v", err)
	}
}

func TestLoadProjectRunConfigDoesNotRepairExplicitInvalidPort(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	raw := `{"host":"127.0.0.1","port":-1,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false}`
	if err := os.WriteFile(projectConfigPath(project), []byte(raw), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	_, _, _, err := loadProjectRunConfig(project)
	if err == nil || !strings.Contains(err.Error(), "1-65535") {
		t.Fatalf("explicit invalid port must be rejected, got %v", err)
	}
}

func createProjectConfigDirectoryLink(t *testing.T, linkPath, targetPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		output, err := exec.Command("cmd", "/c", "mklink", "/J", linkPath, targetPath).CombinedOutput()
		if err != nil {
			t.Skipf("Windows junction unavailable: %v output=%s", err, output)
		}
		return
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
}

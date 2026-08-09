package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	expected, err := filepath.Abs(project)
	if err != nil {
		t.Fatalf("resolve project path: %v", err)
	}
	if cfg.ProjectRoot != expected {
		t.Fatalf("trusted root must follow resolved --dir, got %q want %q", cfg.ProjectRoot, expected)
	}
}

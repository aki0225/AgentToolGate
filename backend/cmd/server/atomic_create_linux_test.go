//go:build linux

package main

import (
	"bytes"
	"os"
	"testing"

	"agenttoolgate/backend/internal/hookassets"
)

func TestCodexRefreshRetainsWritesThroughOldOpenHandle(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	adapterPath := projectCodexHookAdapterPath(project)
	oldCore := []byte("# old core\n")
	if err := os.WriteFile(corePath, oldCore, 0o600); err != nil {
		t.Fatalf("write old core: %v", err)
	}
	if err := os.WriteFile(adapterPath, []byte("# old adapter\n"), 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}
	handle, err := os.OpenFile(corePath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open old core handle: %v", err)
	}
	defer handle.Close()

	report := &projectInitReport{}
	if err := writeCodexRuntimeFiles(project, report, true); err != nil {
		t.Fatalf("refresh hooks: %v", err)
	}
	backupPath := backupContaining(t, report.Backups, oldCore)
	appendix := []byte("# write through old handle\n")
	if _, err := handle.Write(appendix); err != nil {
		t.Fatalf("write through old core handle: %v", err)
	}
	if err := handle.Sync(); err != nil {
		t.Fatalf("sync old core handle: %v", err)
	}
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read retained core backup: %v", err)
	}
	if !bytes.Equal(got, append(oldCore, appendix...)) {
		t.Fatalf("old-handle write was not retained in backup: %q", got)
	}
	if bytes.Equal(got, hookassets.Codex().Core) {
		t.Fatal("recovery backup must contain the displaced core, not the installed bundle")
	}
}

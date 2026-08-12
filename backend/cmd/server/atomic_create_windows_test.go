//go:build windows

package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/windows"
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
	pathPointer, err := windows.UTF16PtrFromString(corePath)
	if err != nil {
		t.Fatalf("encode core path: %v", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("open old core handle: %v", err)
	}
	defer windows.CloseHandle(handle)
	if _, err := windows.SetFilePointer(handle, 0, nil, windows.FILE_END); err != nil {
		t.Fatalf("seek old core handle: %v", err)
	}

	appendix := []byte("# write through old handle\n")
	originalReplace := replaceCodexRuntimeFile
	replaceCodexRuntimeFile = func(root, path string, data []byte, perm os.FileMode, snapshot codexRuntimeFileSnapshot) (string, codexRuntimeFileSnapshot, error) {
		backup, installed, err := replaceCodexRuntimeFileFromSnapshot(root, path, data, perm, snapshot)
		if err != nil || path != corePath {
			return backup, installed, err
		}
		var written uint32
		if err := windows.WriteFile(handle, appendix, &written, nil); err != nil {
			return "", codexRuntimeFileSnapshot{}, err
		}
		if written != uint32(len(appendix)) {
			return "", codexRuntimeFileSnapshot{}, io.ErrShortWrite
		}
		if err := windows.FlushFileBuffers(handle); err != nil {
			return "", codexRuntimeFileSnapshot{}, err
		}
		return backup, installed, nil
	}
	t.Cleanup(func() { replaceCodexRuntimeFile = originalReplace })

	report := &projectInitReport{}
	if err := writeCodexRuntimeFiles(project, report, true); err != nil {
		t.Fatalf("refresh hooks: %v", err)
	}
	backupPath := backupContaining(t, report.Backups, bytes.Join([][]byte{oldCore, appendix}, nil))
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read retained core backup: %v", err)
	}
	if !bytes.Equal(got, append(oldCore, appendix...)) {
		t.Fatalf("old-handle write was not retained in backup: %q", got)
	}
}

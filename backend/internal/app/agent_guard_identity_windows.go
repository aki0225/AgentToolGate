//go:build windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func resolveAgentGuardPathIdentity(path string) (string, string, bool) {
	candidate := strings.TrimSpace(path)
	if candidate == "" {
		return "", "", false
	}

	if absPath, err := filepath.Abs(candidate); err == nil && strings.TrimSpace(absPath) != "" {
		candidate = absPath
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil && strings.TrimSpace(resolved) != "" {
		candidate = resolved
	}
	candidate = filepath.Clean(candidate)
	if candidate == "" || candidate == "." {
		return "", "", false
	}

	file, err := os.Open(candidate)
	if err != nil {
		return "", "", false
	}
	defer file.Close()

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return "", "", false
	}

	fileIdentity := fmt.Sprintf("%08x-%08x-%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	if strings.TrimSpace(fileIdentity) == "" {
		return "", "", false
	}
	return normalizeAgentGuardTarget(candidate), fileIdentity, true
}

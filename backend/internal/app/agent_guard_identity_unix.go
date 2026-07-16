//go:build !windows

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

	info, err := os.Stat(candidate)
	if err != nil {
		return "", "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", false
	}

	fileIdentity := fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	if strings.TrimSpace(fileIdentity) == "" {
		return "", "", false
	}
	return normalizeAgentGuardTarget(candidate), fileIdentity, true
}

//go:build linux

package main

import "golang.org/x/sys/unix"

func moveProjectFileNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}

func replaceProjectFileKeepingBackup(targetPath, replacementPath string) (string, error) {
	if err := unix.Renameat2(unix.AT_FDCWD, replacementPath, unix.AT_FDCWD, targetPath, unix.RENAME_EXCHANGE); err != nil {
		return "", err
	}
	return replacementPath, nil
}

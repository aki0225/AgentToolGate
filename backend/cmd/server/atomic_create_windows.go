//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var replaceFileProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func moveProjectFileNoReplace(oldPath, newPath string) error {
	from, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func replaceProjectFileKeepingBackup(targetPath, replacementPath string) (string, error) {
	backupPath := replacementPath + ".previous"
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return "", err
	}
	replacement, err := windows.UTF16PtrFromString(replacementPath)
	if err != nil {
		return "", err
	}
	backup, err := windows.UTF16PtrFromString(backupPath)
	if err != nil {
		return "", err
	}
	result, _, callErr := replaceFileProc.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(unsafe.Pointer(replacement)),
		uintptr(unsafe.Pointer(backup)),
		1, // REPLACEFILE_WRITE_THROUGH
		0,
		0,
	)
	if result == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return "", callErr
		}
		return "", syscall.EINVAL
	}
	return backupPath, nil
}

//go:build !linux && !windows

package main

import "fmt"

func moveProjectFileNoReplace(_, _ string) error {
	return fmt.Errorf("当前平台不支持原子且不覆盖的文件发布")
}

func replaceProjectFileKeepingBackup(_, _ string) (string, error) {
	return "", fmt.Errorf("当前平台不支持保留备份的原子文件替换")
}

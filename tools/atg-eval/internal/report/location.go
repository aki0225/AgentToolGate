package report

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrOutputExists = errors.New("评估输出目录已存在")

type Location struct {
	Output      string
	SandboxBase string
}

func NormalizeLocation(output, sandboxBase string) (Location, error) {
	outputAbsolute, err := normalizeAbsolutePath(output, "output")
	if err != nil {
		return Location{}, err
	}
	sandboxAbsolute, err := normalizeAbsolutePath(sandboxBase, "sandbox base")
	if err != nil {
		return Location{}, err
	}
	if filepath.Dir(outputAbsolute) == outputAbsolute {
		return Location{}, fmt.Errorf("output 不能是文件系统根目录")
	}
	if _, err := os.Lstat(outputAbsolute); err == nil {
		return Location{}, ErrOutputExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Location{}, fmt.Errorf("检查 output 失败")
	}
	outputResolved, err := resolveWithMissingTail(outputAbsolute)
	if err != nil {
		return Location{}, fmt.Errorf("解析 output 路径失败")
	}
	if !samePath(outputAbsolute, outputResolved) {
		return Location{}, fmt.Errorf("output 不能经过符号链接或目录联接重定向")
	}
	sandboxResolved, err := resolveWithMissingTail(sandboxAbsolute)
	if err != nil {
		return Location{}, fmt.Errorf("解析 sandbox base 路径失败")
	}
	if !samePath(sandboxAbsolute, sandboxResolved) {
		return Location{}, fmt.Errorf("sandbox base 不能经过符号链接或目录联接重定向")
	}
	if pathsOverlap(outputResolved, sandboxResolved) {
		return Location{}, fmt.Errorf("output 与 sandbox base 不能相同或互为父目录")
	}
	return Location{Output: outputResolved, SandboxBase: sandboxResolved}, nil
}

func normalizeAbsolutePath(value, label string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s 不能为空", label)
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("解析 %s 绝对路径失败", label)
	}
	return filepath.Clean(absolute), nil
}

func resolveWithMissingTail(path string) (string, error) {
	current := filepath.Clean(path)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			for index := len(tail) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, tail[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

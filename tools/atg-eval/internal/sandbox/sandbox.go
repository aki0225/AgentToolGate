package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	markerFileName = ".atg-evaluation-root.json"
	markerVersion  = "v1"
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)

type Root struct {
	path   string
	runID  string
	nonce  string
	marker string
}

type markerDocument struct {
	SchemaVersion string `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Nonce         string `json:"nonce"`
	Root          string `json:"root"`
	CreatedAt     string `json:"createdAt"`
}

// Create 在给定 base 目录下创建一个全新的评估根目录。
// 已存在的 runID 会失败，避免复用旧目录后误删其他运行留下的证据。
func Create(baseDir, runID string) (*Root, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, fmt.Errorf("runID 必须匹配 %s", runIDPattern.String())
	}
	baseAbsolute, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("解析 sandbox base 失败：%w", err)
	}
	baseAbsolute = filepath.Clean(baseAbsolute)
	if err := validateDangerousRoot(baseAbsolute); err != nil {
		return nil, fmt.Errorf("sandbox base 不安全：%w", err)
	}
	if err := os.MkdirAll(baseAbsolute, 0o700); err != nil {
		return nil, fmt.Errorf("创建 sandbox base 失败：%w", err)
	}
	baseResolved, err := filepath.EvalSymlinks(baseAbsolute)
	if err != nil {
		return nil, fmt.Errorf("解析 sandbox base 符号链接失败：%w", err)
	}
	baseResolved, err = filepath.Abs(baseResolved)
	if err != nil {
		return nil, fmt.Errorf("规范化 sandbox base 失败：%w", err)
	}
	if !samePath(baseResolved, baseAbsolute) {
		return nil, fmt.Errorf("sandbox base 不能经过符号链接或目录联接重定向")
	}
	if err := validateDangerousRoot(baseResolved); err != nil {
		return nil, fmt.Errorf("解析后的 sandbox base 不安全：%w", err)
	}

	candidate := filepath.Join(baseResolved, runID)
	if err := ensureContained(baseResolved, candidate, false); err != nil {
		return nil, fmt.Errorf("sandbox root 越界：%w", err)
	}
	if err := os.Mkdir(candidate, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("sandbox runID 已存在：%s", runID)
		}
		return nil, fmt.Errorf("创建 sandbox root 失败：%w", err)
	}
	candidateResolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		_ = os.Remove(candidate)
		return nil, fmt.Errorf("解析 sandbox root 失败：%w", err)
	}
	candidateResolved, err = filepath.Abs(candidateResolved)
	if err != nil {
		_ = os.Remove(candidate)
		return nil, fmt.Errorf("规范化 sandbox root 失败：%w", err)
	}
	if err := validateDangerousRoot(candidateResolved); err != nil {
		_ = os.Remove(candidate)
		return nil, fmt.Errorf("sandbox root 不安全：%w", err)
	}
	if err := ensureContained(baseResolved, candidateResolved, false); err != nil {
		_ = os.Remove(candidate)
		return nil, fmt.Errorf("解析后的 sandbox root 越界：%w", err)
	}

	nonce, err := randomNonce()
	if err != nil {
		_ = os.Remove(candidateResolved)
		return nil, fmt.Errorf("生成 sandbox 标记失败：%w", err)
	}
	root := &Root{
		path:   candidateResolved,
		runID:  runID,
		nonce:  nonce,
		marker: filepath.Join(candidateResolved, markerFileName),
	}
	document := markerDocument{
		SchemaVersion: markerVersion,
		RunID:         runID,
		Nonce:         nonce,
		Root:          candidateResolved,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		_ = os.Remove(candidateResolved)
		return nil, fmt.Errorf("编码 sandbox 标记失败：%w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(root.marker, raw, 0o600); err != nil {
		_ = os.Remove(candidateResolved)
		return nil, fmt.Errorf("写入 sandbox 标记失败：%w", err)
	}
	return root, nil
}

func (r *Root) Path() string {
	return r.path
}

func (r *Root) Resolve(relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" {
		return "", fmt.Errorf("sandbox 相对路径不能为空")
	}
	if filepath.IsAbs(trimmed) ||
		filepath.VolumeName(trimmed) != "" ||
		strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, `\`) {
		return "", fmt.Errorf("sandbox 路径必须是相对路径")
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("sandbox 路径不能指向根目录或包含上级跳转")
	}
	candidate := filepath.Join(r.path, cleaned)
	if err := ensureContained(r.path, candidate, false); err != nil {
		return "", err
	}
	resolved, err := resolveWithMissingTail(candidate)
	if err != nil {
		return "", fmt.Errorf("解析 sandbox 路径失败：%w", err)
	}
	if err := ensureContained(r.path, resolved, false); err != nil {
		return "", fmt.Errorf("sandbox 路径经符号链接解析后越界：%w", err)
	}
	return resolved, nil
}

func (r *Root) ResolveTarget(target string) (string, error) {
	const placeholder = "<sandbox>"
	trimmed := strings.TrimSpace(target)
	if trimmed == placeholder {
		return "", fmt.Errorf("动作不能直接指向 sandbox 根目录")
	}
	if strings.HasPrefix(trimmed, placeholder+"/") || strings.HasPrefix(trimmed, placeholder+`\`) {
		trimmed = strings.TrimLeft(strings.TrimPrefix(trimmed, placeholder), `/\`)
	}
	return r.Resolve(trimmed)
}

// Cleanup 只删除带有本次随机 nonce 标记且路径仍与创建时一致的 run root。
// 任一证据不一致都 fail closed，不尝试“尽力清理”。
func (r *Root) Cleanup() error {
	if err := validateDangerousRoot(r.path); err != nil {
		return fmt.Errorf("拒绝清理不安全 sandbox root：%w", err)
	}
	currentResolved, err := filepath.EvalSymlinks(r.path)
	if err != nil {
		return fmt.Errorf("清理前解析 sandbox root 失败：%w", err)
	}
	currentResolved, err = filepath.Abs(currentResolved)
	if err != nil {
		return fmt.Errorf("清理前规范化 sandbox root 失败：%w", err)
	}
	if !samePath(currentResolved, r.path) {
		return fmt.Errorf("sandbox root 已被替换或重定向")
	}

	markerResolved, err := filepath.EvalSymlinks(r.marker)
	if err != nil {
		return fmt.Errorf("解析 sandbox 标记路径失败：%w", err)
	}
	markerResolved, err = filepath.Abs(markerResolved)
	if err != nil {
		return fmt.Errorf("规范化 sandbox 标记路径失败：%w", err)
	}
	if !samePath(markerResolved, r.marker) {
		return fmt.Errorf("sandbox 标记不能是符号链接或目录联接")
	}
	if err := ensureContained(r.path, markerResolved, false); err != nil {
		return fmt.Errorf("sandbox 标记路径越界：%w", err)
	}

	document, err := readMarker(markerResolved)
	if err != nil {
		return err
	}
	if document.SchemaVersion != markerVersion ||
		document.RunID != r.runID ||
		document.Nonce != r.nonce ||
		!samePath(document.Root, r.path) {
		return fmt.Errorf("sandbox 标记与当前运行不匹配")
	}
	if err := os.RemoveAll(r.path); err != nil {
		return fmt.Errorf("清理 sandbox root 失败：%w", err)
	}
	return nil
}

func readMarker(path string) (markerDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return markerDocument{}, fmt.Errorf("读取 sandbox 标记失败：%w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, 16*1024))
	decoder.DisallowUnknownFields()
	var document markerDocument
	if err := decoder.Decode(&document); err != nil {
		return markerDocument{}, fmt.Errorf("解析 sandbox 标记失败：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return markerDocument{}, fmt.Errorf("sandbox 标记包含多余 JSON")
		}
		return markerDocument{}, fmt.Errorf("sandbox 标记尾部无效：%w", err)
	}
	return document, nil
}

func resolveWithMissingTail(path string) (string, error) {
	current := filepath.Clean(path)
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if samePath(parent, current) {
			return "", fmt.Errorf("找不到可解析的已存在父目录")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Abs(resolved)
}

func ensureContained(root, candidate string, allowRoot bool) error {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	candidateAbsolute, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, candidateAbsolute)
	if err != nil {
		return err
	}
	if relative == "." {
		if allowRoot {
			return nil
		}
		return fmt.Errorf("目标不能是 sandbox 根目录")
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("目标不在 sandbox root 内")
	}
	return nil
}

func validateDangerousRoot(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absolute = filepath.Clean(absolute)
	volumeRoot := filepath.VolumeName(absolute) + string(os.PathSeparator)
	if samePath(absolute, volumeRoot) {
		return fmt.Errorf("路径不能是文件系统根目录")
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(absolute, filepath.Clean(home)) {
		return fmt.Errorf("路径不能是用户目录")
	}
	if cwd, err := os.Getwd(); err == nil {
		cwd, _ = filepath.Abs(cwd)
		if samePath(absolute, filepath.Clean(cwd)) {
			return fmt.Errorf("路径不能是当前工作目录")
		}
		if repoRoot := findRepositoryRoot(cwd); repoRoot != "" && samePath(absolute, repoRoot) {
			return fmt.Errorf("路径不能是仓库根目录")
		}
	}
	return nil
}

func findRepositoryRoot(start string) string {
	current := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			absolute, absErr := filepath.Abs(current)
			if absErr == nil {
				return filepath.Clean(absolute)
			}
			return current
		}
		parent := filepath.Dir(current)
		if samePath(parent, current) {
			return ""
		}
		current = parent
	}
}

func randomNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

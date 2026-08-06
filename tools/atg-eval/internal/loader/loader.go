package loader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"agenttoolgate/evaluation/internal/model"
)

const MaxCaseLineBytes = 1 << 20

func LoadFile(path string) ([]model.Case, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开评估用例失败：%w", err)
	}
	defer file.Close()

	cases, err := Load(file)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	return cases, nil
}

// Load 严格解析 JSONL。未知字段和重复用例 ID 都会失败，避免格式漂移被静默忽略。
func Load(reader io.Reader) ([]model.Case, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), MaxCaseLineBytes)

	var cases []model.Case
	seenIDs := make(map[string]int)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		c, err := decodeCase(line)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", lineNumber, err)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("第 %d 行用例 %q：%w", lineNumber, c.ID, err)
		}
		if previousLine, exists := seenIDs[c.ID]; exists {
			return nil, fmt.Errorf("第 %d 行用例 ID %q 与第 %d 行重复", lineNumber, c.ID, previousLine)
		}
		seenIDs[c.ID] = lineNumber
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "token too long") ||
			errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("单行超过 %d 字节限制", MaxCaseLineBytes)
		}
		return nil, fmt.Errorf("扫描 JSONL 失败：%w", err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("JSONL 不包含评估用例")
	}
	return cases, nil
}

func decodeCase(line []byte) (model.Case, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()

	var c model.Case
	if err := decoder.Decode(&c); err != nil {
		return model.Case{}, fmt.Errorf("JSON 无效：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return model.Case{}, fmt.Errorf("一行只能包含一个 JSON 对象")
		}
		return model.Case{}, fmt.Errorf("JSON 尾部无效：%w", err)
	}
	return c, nil
}

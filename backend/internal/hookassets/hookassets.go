package hookassets

import _ "embed"

// CodexBundle 是 init codex 写入目标项目的自包含 Hook 文件。
type CodexBundle struct {
	Adapter []byte
	Core    []byte
}

//go:embed assets/codex/agent-guard-pretool.py
var codexAdapter []byte

//go:embed assets/codex/_guard_core.py
var codexCore []byte

// Codex 返回副本，避免调用方修改进程内嵌资源。
func Codex() CodexBundle {
	return CodexBundle{
		Adapter: append([]byte(nil), codexAdapter...),
		Core:    append([]byte(nil), codexCore...),
	}
}

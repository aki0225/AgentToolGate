package operations

import (
	"fmt"
	"os"
	"path/filepath"

	"agenttoolgate/evaluation/internal/sandbox"
)

type GuardInput struct {
	Client         string `json:"client"`
	ToolName       string `json:"toolName"`
	ActionType     string `json:"actionType"`
	CWD            string `json:"cwd"`
	ProjectRoot    string `json:"projectRoot"`
	Command        string `json:"command"`
	Target         string `json:"target"`
	ContentPreview string `json:"contentPreview"`
	NetworkMethod  string `json:"networkMethod"`
	NetworkURL     string `json:"networkUrl"`
}

type Observation struct {
	Attempted     bool
	Observed      bool
	UpstreamCalls int
	SensitiveLeak bool
	Detail        string
}

type Environment struct {
	Root            *sandbox.Root
	CaseID          string
	Variant         string
	MockURL         string
	SyntheticSecret string
}

func (e Environment) Resolve(relative string) (string, error) {
	return e.Root.Resolve(filepath.Join("cases", e.CaseID, e.Variant, relative))
}

func Prepare(environment Environment) error {
	workspace, err := environment.Resolve("workspace")
	if err != nil {
		return err
	}
	for _, directory := range []string{
		workspace,
		filepath.Join(workspace, "src"),
		filepath.Join(workspace, "docs"),
		filepath.Join(workspace, ".tmp"),
		filepath.Join(workspace, ".git", "hooks"),
		filepath.Join(workspace, ".codex", "hooks"),
		filepath.Join(workspace, ".claude", "hooks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("创建评估 fixture 目录失败：%w", err)
		}
	}
	fixtures := map[string]string{
		filepath.Join(workspace, "src", "main.go"): "package main\n\nfunc main() {}\n",
		filepath.Join(workspace, "README.md"):      "# Synthetic workspace\n",
		filepath.Join(workspace, "old-name.txt"):   "rename fixture\n",
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("写入评估 fixture 失败：%w", err)
		}
	}
	return nil
}

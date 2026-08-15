package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agenttoolgate/backend/internal/guard"
)

type projectValidationReport struct {
	Valid     bool                  `json:"valid"`
	Config    projectValidationFile `json:"config"`
	Protected projectValidationFile `json:"protected"`
	Conflicts []string              `json:"conflicts"`
}

type projectValidationFile struct {
	Path   string   `json:"path"`
	Status string   `json:"status"`
	Errors []string `json:"errors"`
}

func runProjectCLI(args []string, stdout, stderr io.Writer) int {
	for len(args) > 0 && strings.TrimSpace(args[0]) == "--" {
		args = args[1:]
	}
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "project" {
		return -1
	}
	if len(args) < 2 || strings.ToLower(strings.TrimSpace(args[1])) != "validate" {
		fmt.Fprintln(stderr, "project 子命令仅支持 validate")
		return 2
	}
	dir, format, err := parseProjectValidateArgs(args[2:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	root, err := resolveProjectRoot(dir)
	if err != nil {
		fmt.Fprintln(stderr, "无法解析项目目录")
		return 2
	}
	report := validateProjectFiles(root)
	if err := writeProjectValidationReport(stdout, report, format); err != nil {
		fmt.Fprintln(stderr, "输出项目校验结果失败")
		return 1
	}
	if report.Valid {
		return 0
	}
	return 1
}

func parseProjectValidateArgs(args []string) (string, string, error) {
	dir := ""
	format := "text"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" || arg == "--" {
			continue
		}
		if arg == "--dir" {
			index++
			if index >= len(args) {
				return "", "", fmt.Errorf("project validate 需要 --dir <repo>")
			}
			dir = strings.TrimSpace(args[index])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--dir="); ok {
			dir = strings.TrimSpace(value)
			continue
		}
		if arg == "--format" {
			index++
			if index >= len(args) {
				return "", "", fmt.Errorf("project validate 需要 --format text 或 --format json")
			}
			format = strings.ToLower(strings.TrimSpace(args[index]))
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--format="); ok {
			format = strings.ToLower(strings.TrimSpace(value))
			continue
		}
		return "", "", fmt.Errorf("project validate 仅支持 --dir 和 --format")
	}
	if dir == "" {
		return "", "", fmt.Errorf("project validate 需要 --dir <repo>")
	}
	if format != "text" && format != "json" {
		return "", "", fmt.Errorf("project validate 的 --format 仅支持 text 或 json")
	}
	return dir, format, nil
}

func validateProjectFiles(root string) projectValidationReport {
	configResult, config, configValid := validateProjectConfigFile(root)
	protectedResult, protection, protectedValid := validateProjectProtectionFile(root)
	conflicts := projectConfigurationConflicts(config, protection, configValid && protectedValid)
	return projectValidationReport{
		Valid:     configValid && protectedValid && len(conflicts) == 0,
		Config:    configResult,
		Protected: protectedResult,
		Conflicts: conflicts,
	}
}

func validateProjectConfigFile(root string) (projectValidationFile, projectRunConfig, bool) {
	result := projectValidationFile{
		Path:   ".agenttoolgate/config.json",
		Status: projectValidationPathStatus(projectConfigPath(root)),
		Errors: []string{},
	}
	if result.Status == "missing" {
		return result, projectRunConfig{}, false
	}
	config, _, _, err := loadProjectRunConfig(root)
	if err != nil {
		result.Status = "invalid"
		result.Errors = append(result.Errors, err.Error())
		return result, projectRunConfig{}, false
	}
	result.Status = "valid"
	return result, config, true
}

func validateProjectProtectionFile(root string) (projectValidationFile, guard.ProjectProtection, bool) {
	result := projectValidationFile{
		Path:   ".agenttoolgate/protected.json",
		Status: projectValidationPathStatus(projectProtectedPath(root)),
		Errors: []string{},
	}
	if result.Status == "missing" {
		return result, guard.ProjectProtection{}, false
	}
	protection, err := guard.LoadProjectProtection(root)
	if err != nil {
		result.Status = "invalid"
		result.Errors = append(result.Errors, err.Error())
		return result, guard.ProjectProtection{}, false
	}
	result.Status = "valid"
	return result, protection, true
}

func projectValidationPathStatus(path string) string {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "invalid"
	}
	return "present"
}

func projectConfigurationConflicts(config projectRunConfig, protection guard.ProjectProtection, comparable bool) []string {
	if !comparable {
		return []string{}
	}
	var conflicts []string
	for name, values := range map[string][2]string{
		"workspace.name":  {config.Workspace.Name, protection.Workspace.Name},
		"workspace.slug":  {config.Workspace.Slug, protection.Workspace.Slug},
		"workspace.orgId": {config.Workspace.OrgID, protection.Workspace.OrgID},
	} {
		if values[1] != "" && values[0] != values[1] {
			conflicts = append(conflicts, fmt.Sprintf("%s 在 config 和 protected 中不一致", name))
		}
	}
	if protection.DefaultMode != "" && config.HookMode != protection.DefaultMode {
		conflicts = append(conflicts, "hookMode 与 protected.localActionFirewall.defaultMode 不一致")
	}
	sortStrings(conflicts)
	return conflicts
}

func writeProjectValidationReport(w io.Writer, report projectValidationReport, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	fmt.Fprintln(w, "AgentToolGate 项目配置校验")
	fmt.Fprintln(w, "==========================")
	writeProjectValidationFileText(w, report.Config)
	writeProjectValidationFileText(w, report.Protected)
	if len(report.Conflicts) > 0 {
		fmt.Fprintln(w, "冲突:")
		for _, conflict := range report.Conflicts {
			fmt.Fprintln(w, "  - "+conflict)
		}
	}
	result := "invalid"
	if report.Valid {
		result = "valid"
	}
	fmt.Fprintln(w, "结果: "+result)
	return nil
}

func writeProjectValidationFileText(w io.Writer, result projectValidationFile) {
	fmt.Fprintln(w, filepath.ToSlash(result.Path)+": "+result.Status)
	for _, message := range result.Errors {
		fmt.Fprintln(w, "  - "+message)
	}
}

func sortStrings(values []string) {
	for left := 0; left < len(values)-1; left++ {
		for right := left + 1; right < len(values); right++ {
			if values[right] < values[left] {
				values[left], values[right] = values[right], values[left]
			}
		}
	}
}

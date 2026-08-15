package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type projectValidationTestReport struct {
	Valid  bool `json:"valid"`
	Config struct {
		Status string `json:"status"`
	} `json:"config"`
	Protected struct {
		Status string `json:"status"`
	} `json:"protected"`
	Conflicts []string `json:"conflicts"`
}

func TestRunProjectValidateReportsFileStates(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeClaude); err != nil {
		t.Fatalf("init project: %v", err)
	}

	report, code, stderr := runProjectValidateJSON(t, project)
	if code != 0 || stderr != "" {
		t.Fatalf("valid project should pass, code=%d stderr=%s", code, stderr)
	}
	if !report.Valid || report.Config.Status != "valid" || report.Protected.Status != "valid" {
		t.Fatalf("unexpected valid report: %+v", report)
	}

	if err := os.Remove(projectConfigPath(project)); err != nil {
		t.Fatalf("remove project config: %v", err)
	}
	report, code, _ = runProjectValidateJSON(t, project)
	if code != 1 || report.Valid || report.Config.Status != "missing" || report.Protected.Status != "valid" {
		t.Fatalf("missing config must be reported separately, code=%d report=%+v", code, report)
	}

	if err := os.WriteFile(projectConfigPath(project), []byte(renderProjectConfigFile(defaultProjectRunConfig(project))), 0o600); err != nil {
		t.Fatalf("restore project config: %v", err)
	}
	if err := os.Remove(projectProtectedPath(project)); err != nil {
		t.Fatalf("remove project protection: %v", err)
	}
	report, code, _ = runProjectValidateJSON(t, project)
	if code != 1 || report.Valid || report.Config.Status != "valid" || report.Protected.Status != "missing" {
		t.Fatalf("missing protection must be reported separately, code=%d report=%+v", code, report)
	}

	if err := os.WriteFile(projectProtectedPath(project), []byte(`{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}`), 0o600); err != nil {
		t.Fatalf("write invalid project protection: %v", err)
	}
	report, code, _ = runProjectValidateJSON(t, project)
	if code != 1 || report.Valid || report.Config.Status != "valid" || report.Protected.Status != "invalid" {
		t.Fatalf("invalid protection must be reported separately, code=%d report=%+v", code, report)
	}
}

func TestRunProjectValidateReportsInvalidAndCrossFileConflicts(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeClaude); err != nil {
		t.Fatalf("init project: %v", err)
	}
	invalid := `{"host":"127.0.0.1","port":8080,"workspace":{"name":"Demo","slug":"demo","orgId":"demo-org"},"hookMode":"dry-run","openBrowser":false,"unknown":true}`
	if err := os.WriteFile(projectConfigPath(project), []byte(invalid), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	report, code, _ := runProjectValidateJSON(t, project)
	if code != 1 || report.Config.Status != "invalid" || report.Protected.Status != "valid" {
		t.Fatalf("invalid config must not hide protected status, code=%d report=%+v", code, report)
	}

	cfg := defaultProjectRunConfig(project)
	cfg.Workspace.OrgID = "other-org"
	if err := os.WriteFile(projectConfigPath(project), []byte(renderProjectConfigFile(cfg)), 0o600); err != nil {
		t.Fatalf("write conflicting config: %v", err)
	}
	report, code, _ = runProjectValidateJSON(t, project)
	if code != 1 || report.Config.Status != "valid" || report.Protected.Status != "valid" || len(report.Conflicts) == 0 {
		t.Fatalf("cross-file conflict must be explicit, code=%d report=%+v", code, report)
	}
}

func TestRunProjectValidateTextUsesRepositoryRelativePaths(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeClaude); err != nil {
		t.Fatalf("init project: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"project", "validate", "--dir", project, "--format", "text"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("validate returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, ".agenttoolgate/config.json: valid") ||
		!strings.Contains(output, ".agenttoolgate/protected.json: valid") {
		t.Fatalf("text report missing file states:\n%s", output)
	}
	if strings.Contains(output, project) {
		t.Fatalf("text report must not print the absolute repository path:\n%s", output)
	}
}

func TestRunProjectValidateRejectsLinkedConfigDirectory(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	cfg := defaultProjectRunConfig(project)
	if err := os.WriteFile(filepath.Join(outside, "config.json"), []byte(renderProjectConfigFile(cfg)), 0o600); err != nil {
		t.Fatalf("write outside config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "protected.json"), []byte(renderProjectProtectedFile(cfg)), 0o600); err != nil {
		t.Fatalf("write outside protection: %v", err)
	}
	createProjectConfigDirectoryLink(t, filepath.Join(project, ".agenttoolgate"), outside)

	report, code, _ := runProjectValidateJSON(t, project)
	if code != 1 || report.Valid || report.Config.Status != "invalid" {
		t.Fatalf("linked project config directory must fail validation, code=%d report=%+v", code, report)
	}
}

func runProjectValidateJSON(t *testing.T, project string) (projectValidationTestReport, int, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"project", "validate", "--dir", project, "--format", "json"}, &stdout, &stderr)
	var report projectValidationTestReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode validation report: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return report, code, stderr.String()
}

package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/metrics"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/redact"
	evalrunner "agenttoolgate/evaluation/internal/runner"
)

func TestPublishWritesVerifiedProofPack(t *testing.T) {
	output := filepath.Join(t.TempDir(), "proof-pack")
	sandboxBase := filepath.Join(t.TempDir(), "sandbox")
	cases, document := reportFixture()
	const secret = "report-synthetic-secret"
	document.Results[1].FailureReason = "synthetic failure token=" + secret
	document.Metrics = metrics.Aggregate(document.Results)

	published, err := Publish(PublishOptions{
		Output:      output,
		SandboxBase: sandboxBase,
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{Secrets: []string{secret}}),
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Output != filepath.Clean(output) || published.Manifest.Outcome != OutcomeFailed {
		t.Fatalf("published=%+v", published)
	}
	resultsRaw, err := os.ReadFile(filepath.Join(output, ResultsFileName))
	if err != nil {
		t.Fatalf("ReadFile(results) error = %v", err)
	}
	if !bytes.Equal(resultsRaw, published.ResultsBytes) || strings.Contains(string(resultsRaw), secret) {
		t.Fatalf("results.json 未保持精确字节或泄露 secret：%s", resultsRaw)
	}
	var results evalrunner.Document
	if err := decodeStrict(resultsRaw, &results); err != nil {
		t.Fatalf("decode results error = %v", err)
	}
	if len(results.Results[0].Evidence) != 1 || len(results.Results[1].Evidence) != 1 ||
		len(results.Results[2].Evidence) != 0 {
		t.Fatalf("evidence 引用数量异常：%+v", results.Results)
	}
	for _, result := range results.Results[:2] {
		ref := result.Evidence[0]
		raw, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(ref.Path)))
		if err != nil {
			t.Fatalf("ReadFile(evidence) error = %v", err)
		}
		if hashBytes(raw) != ref.SHA256 || strings.Contains(string(raw), secret) {
			t.Fatalf("evidence 摘要或脱敏异常：%s", raw)
		}
		var evidence Evidence
		if err := json.Unmarshal(raw, &evidence); err != nil || evidence.Validate() != nil {
			t.Fatalf("evidence 无效：decode=%v validate=%v", err, evidence.Validate())
		}
	}
	manifestRaw, err := os.ReadFile(filepath.Join(output, ManifestFileName))
	if err != nil {
		t.Fatalf("ReadFile(manifest) error = %v", err)
	}
	var manifest Manifest
	if err := decodeStrict(manifestRaw, &manifest); err != nil || manifest.Validate() != nil {
		t.Fatalf("manifest 无效：decode=%v validate=%v", err, manifest.Validate())
	}
	wantMediaTypes := map[string]string{
		JUnitFileName:   mediaTypeXML,
		SummaryFileName: mediaTypeMarkdown,
		HTMLFileName:    mediaTypeHTML,
	}
	for _, file := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(file.Path)))
		if err != nil || int64(len(raw)) != file.SizeBytes || hashBytes(raw) != file.SHA256 {
			t.Fatalf("manifest 文件摘要异常：%+v err=%v", file, err)
		}
		if want, exists := wantMediaTypes[file.Path]; exists {
			if file.MediaType != want {
				t.Fatalf("%s mediaType=%q want=%q", file.Path, file.MediaType, want)
			}
			delete(wantMediaTypes, file.Path)
		}
	}
	if len(wantMediaTypes) != 0 {
		t.Fatalf("缺少人读报告：%v", wantMediaTypes)
	}
}

func TestPublishDoesNotOverwriteExistingOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "proof-pack")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	marker := filepath.Join(output, "marker.txt")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	cases, document := reportFixture()
	_, err := Publish(PublishOptions{
		Output:      output,
		SandboxBase: filepath.Join(t.TempDir(), "sandbox"),
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{}),
	})
	if !errors.Is(err, ErrOutputExists) {
		t.Fatalf("Publish() error=%v want ErrOutputExists", err)
	}
	raw, readErr := os.ReadFile(marker)
	if readErr != nil || string(raw) != "unchanged" {
		t.Fatalf("既有 output 被修改：raw=%q err=%v", raw, readErr)
	}
}

func TestPublishPreparedCleansStagingAfterWriteFailure(t *testing.T) {
	cases, document := reportFixture()
	prepared, err := prepareReport(PublishOptions{
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{}),
	})
	if err != nil {
		t.Fatalf("prepareReport() error = %v", err)
	}
	prepared.evidence[0].path = EvidenceDirName + "/missing/evidence.json"

	parent := t.TempDir()
	output := filepath.Join(parent, "proof-pack")
	if err := publishPrepared(output, prepared); err == nil {
		t.Fatal("staging 写入失败时 publishPrepared() 必须返回错误")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("发布失败不得留下最终目录：%v", err)
	}
	assertNoStagingDirectories(t, parent)
}

func TestPublishAllowsSingleAtomicWinner(t *testing.T) {
	cases, document := reportFixture()
	parent := t.TempDir()
	options := PublishOptions{
		Output:      filepath.Join(parent, "proof-pack"),
		SandboxBase: filepath.Join(t.TempDir(), "sandbox"),
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{}),
	}

	start := make(chan struct{})
	errorsByRun := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := Publish(options)
			errorsByRun <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByRun)

	var succeeded, outputExists int
	for err := range errorsByRun {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOutputExists):
			outputExists++
		default:
			t.Fatalf("并发发布返回意外错误：%v", err)
		}
	}
	if succeeded != 1 || outputExists != 1 {
		t.Fatalf("并发发布结果异常：succeeded=%d outputExists=%d", succeeded, outputExists)
	}
	if _, err := os.Stat(filepath.Join(options.Output, ResultsFileName)); err != nil {
		t.Fatalf("获胜发布缺少 results.json：%v", err)
	}
	assertNoStagingDirectories(t, parent)
}

func TestPublishedSchemasContainValidJSON(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位当前测试文件")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", "..", ".."))
	for _, name := range []string{
		"results.schema.json",
		"result.schema.json",
		"evidence.schema.json",
		"run-manifest.schema.json",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(repositoryRoot, "evaluation", "schema", name))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("schema 不是有效 JSON：%v", err)
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("$schema=%v", schema["$schema"])
			}
		})
	}
}

func TestPrepareReportRejectsMetricDrift(t *testing.T) {
	cases, document := reportFixture()
	document.Metrics.PassedCount++
	_, err := prepareReport(PublishOptions{
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{}),
	})
	if err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("metrics 漂移必须被拒绝，error=%v", err)
	}
}

func TestManifestValidationRejectsUnsortedOrSelfHashedFiles(t *testing.T) {
	_, document := reportFixture()
	manifest := Manifest{
		SchemaVersion: model.SchemaVersionV1,
		RunID:         document.RunID,
		Platform:      document.Platform,
		StartedAt:     document.StartedAt,
		CompletedAt:   document.CompletedAt,
		Outcome:       OutcomePassed,
		Schemas: SchemaRefs{
			Results:  "evaluation/schema/results.schema.json",
			Result:   "evaluation/schema/result.schema.json",
			Evidence: "evaluation/schema/evidence.schema.json",
			Manifest: "evaluation/schema/run-manifest.schema.json",
		},
		Suites: []SuiteEntry{{
			ID:          model.SuiteBenignDevelopmentV1,
			CaseCount:   1,
			InputSHA256: strings.Repeat("a", 64),
		}},
		Files: []FileEntry{{
			Path:      ManifestFileName,
			SizeBytes: 1,
			SHA256:    strings.Repeat("b", 64),
			MediaType: "application/json",
		}},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("manifest 不得登记自身哈希")
	}
}

func TestManifestValidationRequiresEveryReport(t *testing.T) {
	cases, document := reportFixture()
	prepared, err := prepareReport(PublishOptions{
		InputSHA256: hashBytes([]byte("synthetic suite\n")),
		Cases:       cases,
		Document:    document,
		Redactor:    redact.New(redact.Options{}),
	})
	if err != nil {
		t.Fatalf("prepareReport() error = %v", err)
	}
	manifest := prepared.manifest
	files := manifest.Files[:0]
	for _, file := range manifest.Files {
		if file.Path != HTMLFileName {
			files = append(files, file)
		}
	}
	manifest.Files = files
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), HTMLFileName) {
		t.Fatalf("缺少 HTML 报告必须失败：%v", err)
	}
}

func reportFixture() ([]model.Case, evalrunner.Document) {
	started := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	cases := []model.Case{
		{
			SchemaVersion: model.SchemaVersionV1,
			ID:            "benign.git-status",
			Suite:         model.SuiteBenignDevelopmentV1,
			Category:      "safe_command",
			Entry:         model.EntryGuardCore,
			Action:        model.Action{Type: model.ActionCommand, Operation: "git_status"},
		},
		{
			SchemaVersion: model.SchemaVersionV1,
			ID:            "governance.ticket-single-use",
			Suite:         model.SuiteGovernanceInvariantsV1,
			Category:      "approval_integrity",
			Entry:         model.EntryGovernance,
			Action:        model.Action{Type: model.ActionGovernance, Operation: "ticket_single_use"},
		},
		{
			SchemaVersion: model.SchemaVersionV1,
			ID:            "dangerous.windows-only",
			Suite:         model.SuiteDangerousActionsV1,
			Category:      "persistence",
			Entry:         model.EntryGuardCore,
			Action:        model.Action{Type: model.ActionWrite, Operation: "write_windows_startup"},
		},
	}
	results := []model.Result{
		{
			SchemaVersion: model.SchemaVersionV1,
			RunID:         "report-test",
			CaseID:        cases[0].ID,
			Suite:         cases[0].Suite,
			Category:      cases[0].Category,
			Platform:      model.PlatformWindows,
			Entry:         cases[0].Entry,
			Status:        model.ResultPassed,
			ExpectedDecision: []model.Decision{
				model.DecisionAllow,
			},
			ActualDecision: model.DecisionAllow,
			DecisionSilent: true,
			Signals:        []string{"safe_command"},
			DurationMS:     1,
			Evidence:       []model.EvidenceRef{},
		},
		{
			SchemaVersion: model.SchemaVersionV1,
			RunID:         "report-test",
			CaseID:        cases[1].ID,
			Suite:         cases[1].Suite,
			Category:      cases[1].Category,
			Platform:      model.PlatformWindows,
			Entry:         cases[1].Entry,
			Status:        model.ResultFailed,
			ExpectedDecision: []model.Decision{
				model.DecisionDeny,
			},
			ActualDecision:        model.DecisionAllow,
			Signals:               []string{"ticket_replay"},
			DurationMS:            2,
			TicketReplaySucceeded: true,
			FailureReason:         "ticket 被重复消费",
			Evidence:              []model.EvidenceRef{},
		},
		{
			SchemaVersion: model.SchemaVersionV1,
			RunID:         "report-test",
			CaseID:        cases[2].ID,
			Suite:         cases[2].Suite,
			Category:      cases[2].Category,
			Platform:      model.PlatformWindows,
			Entry:         cases[2].Entry,
			Status:        model.ResultSkipped,
			ExpectedDecision: []model.Decision{
				model.DecisionDeny,
			},
			Signals:    []string{},
			SkipReason: "当前平台不适用",
			Evidence:   []model.EvidenceRef{},
		},
	}
	document := evalrunner.Document{
		SchemaVersion: model.SchemaVersionV1,
		RunID:         "report-test",
		Platform:      model.PlatformWindows,
		StartedAt:     started.Format(time.RFC3339Nano),
		CompletedAt:   started.Add(time.Second).Format(time.RFC3339Nano),
		Results:       results,
		Metrics:       metrics.Aggregate(results),
	}
	return cases, document
}

func TestCloneDocumentDoesNotAliasResults(t *testing.T) {
	_, document := reportFixture()
	cloned := cloneDocument(document)
	cloned.Results[0].Signals[0] = "changed"
	cloned.Results[0].ExpectedDecision[0] = model.DecisionDeny
	if reflect.DeepEqual(cloned, document) || document.Results[0].Signals[0] == "changed" {
		t.Fatal("cloneDocument 不得共享可变切片")
	}
}

func assertNoStagingDirectories(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".atg-eval-staging-") {
			t.Fatalf("发布后残留 staging 目录：%s", entry.Name())
		}
	}
}

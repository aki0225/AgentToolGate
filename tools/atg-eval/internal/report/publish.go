package report

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/metrics"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/redact"
	evalrunner "agenttoolgate/evaluation/internal/runner"
)

const (
	maxEvidenceFileBytes  = 256 * 1024
	maxEvidenceTotalBytes = 8 * 1024 * 1024
	maxResultsBytes       = 8 * 1024 * 1024
)

type PublishOptions struct {
	Output      string
	SandboxBase string
	InputSHA256 string
	Cases       []model.Case
	Document    evalrunner.Document
	Redactor    *redact.Redactor
}

type Published struct {
	Output       string
	ResultsBytes []byte
	Manifest     Manifest
}

type evidenceArtifact struct {
	path     string
	evidence Evidence
	bytes    []byte
}

type preparedReport struct {
	document      evalrunner.Document
	resultsBytes  []byte
	evidence      []evidenceArtifact
	manifest      Manifest
	manifestBytes []byte
}

func Publish(options PublishOptions) (Published, error) {
	if options.Redactor == nil {
		return Published{}, fmt.Errorf("缺少报告脱敏器")
	}
	location, err := NormalizeLocation(options.Output, options.SandboxBase)
	if err != nil {
		return Published{}, err
	}
	prepared, err := prepareReport(options)
	if err != nil {
		return Published{}, err
	}
	if err := publishPrepared(location.Output, prepared); err != nil {
		return Published{}, err
	}
	return Published{
		Output:       location.Output,
		ResultsBytes: append([]byte(nil), prepared.resultsBytes...),
		Manifest:     prepared.manifest,
	}, nil
}

func prepareReport(options PublishOptions) (preparedReport, error) {
	document := cloneDocument(options.Document)
	if err := validateDocument(document, options.Cases, false); err != nil {
		return preparedReport{}, fmt.Errorf("原始结果文档无效：%w", err)
	}

	artifacts := make([]evidenceArtifact, 0, len(document.Results))
	var evidenceTotal int
	for index := range document.Results {
		result := &document.Results[index]
		if result.Status == model.ResultSkipped {
			result.Evidence = []model.EvidenceRef{}
			continue
		}
		evidence := evidenceFor(options.Cases[index], *result, document.CompletedAt)
		validated, raw, err := encodeRedacted(evidence, options.Redactor, func(value Evidence) error {
			return value.Validate()
		})
		if err != nil {
			return preparedReport{}, fmt.Errorf("生成 %s evidence 失败：%w", result.CaseID, err)
		}
		if len(raw) > maxEvidenceFileBytes {
			return preparedReport{}, fmt.Errorf("%s evidence 超过 %d 字节", result.CaseID, maxEvidenceFileBytes)
		}
		evidenceTotal += len(raw)
		if evidenceTotal > maxEvidenceTotalBytes {
			return preparedReport{}, fmt.Errorf("evidence 总大小超过 %d 字节", maxEvidenceTotalBytes)
		}
		path := EvidenceDirName + "/" + result.CaseID + ".json"
		result.Evidence = []model.EvidenceRef{{
			Kind:   validated.Kind,
			Path:   path,
			SHA256: hashBytes(raw),
		}}
		artifacts = append(artifacts, evidenceArtifact{path: path, evidence: validated, bytes: raw})
	}
	document.Metrics = metrics.Aggregate(document.Results)
	validatedDocument, resultsBytes, err := encodeRedacted(document, options.Redactor, func(value evalrunner.Document) error {
		return validateDocument(value, options.Cases, true)
	})
	if err != nil {
		return preparedReport{}, fmt.Errorf("生成 results.json 失败：%w", err)
	}
	if len(resultsBytes) > maxResultsBytes {
		return preparedReport{}, fmt.Errorf("results.json 超过 %d 字节", maxResultsBytes)
	}
	if err := validateEvidenceSet(validatedDocument, options.Cases, artifacts); err != nil {
		return preparedReport{}, err
	}

	manifest := buildManifest(validatedDocument, options.Cases, options.InputSHA256, resultsBytes, artifacts)
	manifestBytes, err := encodeCanonical(manifest)
	if err != nil {
		return preparedReport{}, err
	}
	var decodedManifest Manifest
	if err := decodeStrict(manifestBytes, &decodedManifest); err != nil {
		return preparedReport{}, fmt.Errorf("严格解析 manifest 失败：%w", err)
	}
	if err := decodedManifest.Validate(); err != nil {
		return preparedReport{}, fmt.Errorf("manifest 无效：%w", err)
	}
	return preparedReport{
		document:      validatedDocument,
		resultsBytes:  resultsBytes,
		evidence:      artifacts,
		manifest:      decodedManifest,
		manifestBytes: manifestBytes,
	}, nil
}

func evidenceFor(c model.Case, result model.Result, capturedAt string) Evidence {
	evidence := Evidence{
		SchemaVersion: model.SchemaVersionV1,
		RunID:         result.RunID,
		CaseID:        result.CaseID,
		Suite:         result.Suite,
		Platform:      result.Platform,
		CapturedAt:    capturedAt,
		Truncated:     false,
	}
	if c.Entry == model.EntryGovernance {
		evidence.Kind = EvidenceGovernance
		evidence.Governance = &GovernanceObservation{
			Operation:                       c.Action.Operation,
			ExpectedDecision:                append([]model.Decision(nil), result.ExpectedDecision...),
			ActualDecision:                  result.ActualDecision,
			RiskLevel:                       result.RiskLevel,
			Signals:                         append([]string(nil), result.Signals...),
			DurationMS:                      result.DurationMS,
			SideEffectObserved:              result.SideEffectObserved,
			UpstreamCallsBeforeApproval:     result.UpstreamCallsBeforeApproval,
			SelfReviewSucceeded:             result.SelfReviewSucceeded,
			FrozenArgumentMutationSucceeded: result.FrozenArgumentMutationSucceeded,
			TicketReplaySucceeded:           result.TicketReplaySucceeded,
			SecretLeakDetected:              result.SecretLeakDetected,
			OfflineHighRiskAllowed:          result.OfflineHighRiskAllowed,
			FailureReason:                   result.FailureReason,
		}
		return evidence
	}
	evidence.Kind = EvidenceAction
	evidence.Action = &ActionObservation{
		Operation:                  c.Action.Operation,
		ActionType:                 c.Action.Type,
		Entry:                      c.Entry,
		ExpectedDecision:           append([]model.Decision(nil), result.ExpectedDecision...),
		ActualDecision:             result.ActualDecision,
		RiskLevel:                  result.RiskLevel,
		DecisionSilent:             result.DecisionSilent,
		Signals:                    append([]string(nil), result.Signals...),
		DurationMS:                 result.DurationMS,
		SideEffectAttempted:        result.SideEffectAttempted,
		BaselineSideEffectObserved: result.BaselineSideEffectObserved,
		SideEffectObserved:         result.SideEffectObserved,
		UpstreamCalls:              result.UpstreamCallsBeforeApproval,
		SecretLeakDetected:         result.SecretLeakDetected,
		FailureReason:              result.FailureReason,
	}
	return evidence
}

func buildManifest(
	document evalrunner.Document,
	cases []model.Case,
	inputHash string,
	resultsBytes []byte,
	artifacts []evidenceArtifact,
) Manifest {
	counts := make(map[string]int)
	for _, c := range cases {
		counts[c.Suite]++
	}
	suites := make([]SuiteEntry, 0, len(counts))
	for suite, count := range counts {
		suites = append(suites, SuiteEntry{ID: suite, CaseCount: count, InputSHA256: inputHash})
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].ID < suites[j].ID })
	files := []FileEntry{{
		Path:      ResultsFileName,
		SizeBytes: int64(len(resultsBytes)),
		SHA256:    hashBytes(resultsBytes),
		MediaType: "application/json",
	}}
	for _, artifact := range artifacts {
		files = append(files, FileEntry{
			Path:      artifact.path,
			SizeBytes: int64(len(artifact.bytes)),
			SHA256:    hashBytes(artifact.bytes),
			MediaType: "application/json",
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	outcome := OutcomePassed
	for _, result := range document.Results {
		if result.Status == model.ResultFailed {
			outcome = OutcomeFailed
			break
		}
	}
	return Manifest{
		SchemaVersion: model.SchemaVersionV1,
		RunID:         document.RunID,
		Platform:      document.Platform,
		StartedAt:     document.StartedAt,
		CompletedAt:   document.CompletedAt,
		Outcome:       outcome,
		Schemas: SchemaRefs{
			Results:  "evaluation/schema/results.schema.json",
			Result:   "evaluation/schema/result.schema.json",
			Evidence: "evaluation/schema/evidence.schema.json",
			Manifest: "evaluation/schema/run-manifest.schema.json",
		},
		Suites: suites,
		Files:  files,
	}
}

func validateDocument(document evalrunner.Document, cases []model.Case, requireEvidence bool) error {
	if document.SchemaVersion != model.SchemaVersionV1 {
		return fmt.Errorf("schemaVersion 必须为 %q", model.SchemaVersionV1)
	}
	if err := ValidateRunID(document.RunID); err != nil {
		return err
	}
	if document.Platform != model.PlatformWindows && document.Platform != model.PlatformLinux {
		return fmt.Errorf("platform 不受支持：%q", document.Platform)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, document.StartedAt)
	if err != nil {
		return fmt.Errorf("startedAt 必须是 RFC3339Nano：%w", err)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, document.CompletedAt)
	if err != nil || completedAt.Before(startedAt) {
		return fmt.Errorf("completedAt 无效或早于 startedAt")
	}
	if len(document.Results) != len(cases) || len(cases) == 0 {
		return fmt.Errorf("results 必须与输入 cases 一一对应")
	}
	seen := make(map[string]struct{}, len(cases))
	for index, result := range document.Results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("results[%d] 无效：%w", index, err)
		}
		if result.RunID != document.RunID || result.Platform != document.Platform ||
			result.CaseID != cases[index].ID || result.Suite != cases[index].Suite {
			return fmt.Errorf("results[%d] 与 document 或输入 case 不一致", index)
		}
		if _, exists := seen[result.CaseID]; exists {
			return fmt.Errorf("caseId 不能重复：%q", result.CaseID)
		}
		seen[result.CaseID] = struct{}{}
		if len(result.Signals) > 512 || len([]byte(result.FailureReason)) > 4096 || len([]byte(result.SkipReason)) > 4096 {
			return fmt.Errorf("results[%d] 文本或列表超过发布限制", index)
		}
		if requireEvidence {
			if result.Status == model.ResultSkipped && len(result.Evidence) != 0 {
				return fmt.Errorf("skipped 结果不得包含 evidence")
			}
			if result.Status != model.ResultSkipped && len(result.Evidence) != 1 {
				return fmt.Errorf("非 skipped 结果必须包含一份 evidence")
			}
		}
	}
	wantMetrics := metrics.Aggregate(document.Results)
	if !reflect.DeepEqual(document.Metrics, wantMetrics) {
		return fmt.Errorf("metrics 与最终 results 聚合结果不一致")
	}
	return nil
}

func validateEvidenceSet(document evalrunner.Document, cases []model.Case, artifacts []evidenceArtifact) error {
	byPath := make(map[string]evidenceArtifact, len(artifacts))
	for _, artifact := range artifacts {
		if _, exists := byPath[artifact.path]; exists {
			return fmt.Errorf("evidence 路径重复：%s", artifact.path)
		}
		byPath[artifact.path] = artifact
	}
	for index, result := range document.Results {
		if result.Status == model.ResultSkipped {
			continue
		}
		ref := result.Evidence[0]
		artifact, ok := byPath[ref.Path]
		if !ok || ref.Kind != artifact.evidence.Kind || ref.SHA256 != hashBytes(artifact.bytes) {
			return fmt.Errorf("results[%d] evidence 引用与实际文件不一致", index)
		}
		if err := evidenceMatches(artifact.evidence, cases[index], result); err != nil {
			return fmt.Errorf("results[%d] evidence 内容不一致：%w", index, err)
		}
		delete(byPath, ref.Path)
	}
	if len(byPath) != 0 {
		return fmt.Errorf("存在未被 results 引用的 evidence")
	}
	return nil
}

func evidenceMatches(evidence Evidence, c model.Case, result model.Result) error {
	if evidence.RunID != result.RunID || evidence.CaseID != result.CaseID ||
		evidence.Suite != result.Suite || evidence.Platform != result.Platform {
		return fmt.Errorf("公共字段不一致")
	}
	if c.Entry == model.EntryGovernance {
		if evidence.Kind != EvidenceGovernance || evidence.Governance == nil ||
			evidence.Governance.Operation != c.Action.Operation ||
			evidence.Governance.ActualDecision != result.ActualDecision ||
			evidence.Governance.FailureReason != result.FailureReason {
			return fmt.Errorf("governance 字段不一致")
		}
		return nil
	}
	if evidence.Kind != EvidenceAction || evidence.Action == nil ||
		evidence.Action.Operation != c.Action.Operation ||
		evidence.Action.ActualDecision != result.ActualDecision ||
		evidence.Action.FailureReason != result.FailureReason {
		return fmt.Errorf("action 字段不一致")
	}
	return nil
}

func publishPrepared(output string, prepared preparedReport) (err error) {
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("创建 output 父目录失败")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("复核 output 父目录失败")
	}
	resolvedParent, err = filepath.Abs(resolvedParent)
	if err != nil || !samePath(parent, resolvedParent) {
		return fmt.Errorf("output 父目录不能经过符号链接或目录联接重定向")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return fmt.Errorf("打开 output 父目录失败")
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	outputName := filepath.Base(output)
	if _, statErr := root.Lstat(outputName); statErr == nil {
		return ErrOutputExists
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("检查 output 目录失败")
	}
	stagingName, err := createStagingDirectory(root)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, root.RemoveAll(stagingName))
		}
	}()
	if err := root.MkdirAll(filepath.Join(stagingName, EvidenceDirName), 0o700); err != nil {
		return fmt.Errorf("创建 staging evidence 目录失败")
	}
	for _, artifact := range prepared.evidence {
		if err := writeRootFile(root, filepath.Join(stagingName, filepath.FromSlash(artifact.path)), artifact.bytes); err != nil {
			return err
		}
	}
	if err := writeRootFile(root, filepath.Join(stagingName, ResultsFileName), prepared.resultsBytes); err != nil {
		return err
	}
	if err := writeRootFile(root, filepath.Join(stagingName, ManifestFileName), prepared.manifestBytes); err != nil {
		return err
	}
	if err := verifyStaging(root, stagingName, prepared); err != nil {
		return err
	}
	if err := root.Rename(stagingName, outputName); err != nil {
		if _, statErr := root.Lstat(outputName); statErr == nil {
			return ErrOutputExists
		}
		return fmt.Errorf("原子发布评估结果失败")
	}
	published = true
	return nil
}

func createStagingDirectory(root *os.Root) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("生成 staging 名称失败")
		}
		name := ".atg-eval-staging-" + hex.EncodeToString(random)
		if err := root.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("创建 staging 目录失败")
		}
	}
	return "", fmt.Errorf("无法分配 staging 目录")
}

func writeRootFile(root *os.Root, path string, raw []byte) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建报告文件失败")
	}
	written, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != len(raw) {
		return errors.Join(fmt.Errorf("写入报告文件失败"), writeErr, closeErr)
	}
	return nil
}

func verifyStaging(root *os.Root, stagingName string, prepared preparedReport) error {
	staging, err := root.OpenRoot(stagingName)
	if err != nil {
		return fmt.Errorf("打开 staging 目录失败")
	}
	defer staging.Close()
	entries, err := readDirectory(staging, ".")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(entries, []string{EvidenceDirName + "/", ResultsFileName, ManifestFileName}) {
		return fmt.Errorf("staging 顶层文件集合不符合契约：%v", entries)
	}
	evidenceRoot, err := staging.OpenRoot(EvidenceDirName)
	if err != nil {
		return fmt.Errorf("打开 staging evidence 目录失败")
	}
	defer evidenceRoot.Close()
	evidenceEntries, err := readDirectory(evidenceRoot, ".")
	if err != nil {
		return err
	}
	wantEvidenceEntries := make([]string, 0, len(prepared.evidence))
	for _, artifact := range prepared.evidence {
		wantEvidenceEntries = append(wantEvidenceEntries, strings.TrimPrefix(artifact.path, EvidenceDirName+"/"))
	}
	sort.Strings(wantEvidenceEntries)
	if !reflect.DeepEqual(evidenceEntries, wantEvidenceEntries) {
		return fmt.Errorf("staging evidence 文件集合不符合契约")
	}
	resultsBytes, err := staging.ReadFile(ResultsFileName)
	if err != nil || !bytes.Equal(resultsBytes, prepared.resultsBytes) {
		return fmt.Errorf("复核 results.json 失败")
	}
	var document evalrunner.Document
	if err := decodeStrict(resultsBytes, &document); err != nil || !reflect.DeepEqual(document, prepared.document) {
		return fmt.Errorf("复核 results.json 语义失败")
	}
	manifestBytes, err := staging.ReadFile(ManifestFileName)
	if err != nil || !bytes.Equal(manifestBytes, prepared.manifestBytes) {
		return fmt.Errorf("复核 run-manifest.json 失败")
	}
	var manifest Manifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil || !reflect.DeepEqual(manifest, prepared.manifest) {
		return fmt.Errorf("复核 run-manifest.json 语义失败")
	}
	for _, artifact := range prepared.evidence {
		raw, readErr := staging.ReadFile(filepath.FromSlash(artifact.path))
		if readErr != nil || !bytes.Equal(raw, artifact.bytes) {
			return fmt.Errorf("复核 evidence 文件失败")
		}
		var evidence Evidence
		if err := decodeStrict(raw, &evidence); err != nil || !reflect.DeepEqual(evidence, artifact.evidence) {
			return fmt.Errorf("复核 evidence 语义失败")
		}
	}
	for _, file := range manifest.Files {
		raw, readErr := staging.ReadFile(filepath.FromSlash(file.Path))
		if readErr != nil || int64(len(raw)) != file.SizeBytes || hashBytes(raw) != file.SHA256 {
			return fmt.Errorf("manifest 文件摘要校验失败：%s", file.Path)
		}
	}
	return nil
}

func readDirectory(root *os.Root, path string) ([]string, error) {
	directory, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取 staging 目录失败")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("枚举 staging 目录失败")
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("staging 不允许符号链接或目录联接")
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		} else if info, infoErr := entry.Info(); infoErr != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("staging 只允许普通文件")
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func encodeRedacted[T any](value T, redactor *redact.Redactor, validate func(T) error) (T, []byte, error) {
	var zero T
	raw, err := json.Marshal(value)
	if err != nil {
		return zero, nil, err
	}
	redacted, err := redactor.JSON(raw)
	if err != nil {
		return zero, nil, err
	}
	var decoded T
	if err := decodeStrict(redacted, &decoded); err != nil {
		return zero, nil, fmt.Errorf("严格解析脱敏 JSON 失败：%w", err)
	}
	if err := validate(decoded); err != nil {
		return zero, nil, fmt.Errorf("脱敏后语义无效：%w", err)
	}
	formatted, err := encodeCanonical(decoded)
	if err != nil {
		return zero, nil, err
	}
	return decoded, formatted, nil
}

func encodeCanonical(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON 只能包含一个值")
		}
		return err
	}
	return nil
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneDocument(document evalrunner.Document) evalrunner.Document {
	cloned := document
	cloned.Results = append([]model.Result(nil), document.Results...)
	for index := range cloned.Results {
		cloned.Results[index].ExpectedDecision = append([]model.Decision(nil), document.Results[index].ExpectedDecision...)
		cloned.Results[index].Signals = append([]string(nil), document.Results[index].Signals...)
		cloned.Results[index].Evidence = append([]model.EvidenceRef(nil), document.Results[index].Evidence...)
	}
	return cloned
}

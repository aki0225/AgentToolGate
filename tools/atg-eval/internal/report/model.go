package report

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/model"
)

const (
	ResultsFileName  = "results.json"
	ManifestFileName = "run-manifest.json"
	EvidenceDirName  = "evidence"
	JUnitFileName    = "junit.xml"
	SummaryFileName  = "summary.md"
	HTMLFileName     = "report.html"
)

const (
	mediaTypeJSON     = "application/json"
	mediaTypeXML      = "application/xml"
	mediaTypeMarkdown = "text/markdown; charset=utf-8"
	mediaTypeHTML     = "text/html; charset=utf-8"
)

const (
	EvidenceAction     = "action_observation"
	EvidenceGovernance = "governance_observation"
	OutcomePassed      = "passed"
	OutcomeFailed      = "failed"
)

var (
	runIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)
	tokenPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Evidence struct {
	SchemaVersion string                 `json:"schemaVersion"`
	RunID         string                 `json:"runId"`
	CaseID        string                 `json:"caseId"`
	Suite         string                 `json:"suite"`
	Platform      model.Platform         `json:"platform"`
	Kind          string                 `json:"kind"`
	CapturedAt    string                 `json:"capturedAt"`
	Truncated     bool                   `json:"truncated"`
	Action        *ActionObservation     `json:"action,omitempty"`
	Governance    *GovernanceObservation `json:"governance,omitempty"`
}

type ActionObservation struct {
	Operation                  string           `json:"operation"`
	ActionType                 model.ActionType `json:"actionType"`
	Entry                      model.Entry      `json:"entry"`
	ExpectedDecision           []model.Decision `json:"expectedDecision"`
	ActualDecision             model.Decision   `json:"actualDecision,omitempty"`
	RiskLevel                  string           `json:"riskLevel,omitempty"`
	DecisionSilent             bool             `json:"decisionSilent"`
	Signals                    []string         `json:"signals"`
	DurationMS                 float64          `json:"durationMs"`
	SideEffectAttempted        bool             `json:"sideEffectAttempted"`
	BaselineSideEffectObserved bool             `json:"baselineSideEffectObserved"`
	SideEffectObserved         bool             `json:"sideEffectObserved"`
	UpstreamCalls              int              `json:"upstreamCalls"`
	SecretLeakDetected         bool             `json:"secretLeakDetected"`
	FailureReason              string           `json:"failureReason,omitempty"`
}

type GovernanceObservation struct {
	Operation                       string           `json:"operation"`
	ExpectedDecision                []model.Decision `json:"expectedDecision"`
	ActualDecision                  model.Decision   `json:"actualDecision,omitempty"`
	RiskLevel                       string           `json:"riskLevel,omitempty"`
	Signals                         []string         `json:"signals"`
	DurationMS                      float64          `json:"durationMs"`
	SideEffectObserved              bool             `json:"sideEffectObserved"`
	UpstreamCallsBeforeApproval     int              `json:"upstreamCallsBeforeApproval"`
	SelfReviewSucceeded             bool             `json:"selfReviewSucceeded"`
	FrozenArgumentMutationSucceeded bool             `json:"frozenArgumentMutationSucceeded"`
	TicketReplaySucceeded           bool             `json:"ticketReplaySucceeded"`
	SecretLeakDetected              bool             `json:"secretLeakDetected"`
	OfflineHighRiskAllowed          bool             `json:"offlineHighRiskAllowed"`
	FailureReason                   string           `json:"failureReason,omitempty"`
}

type Manifest struct {
	SchemaVersion string         `json:"schemaVersion"`
	RunID         string         `json:"runId"`
	Platform      model.Platform `json:"platform"`
	StartedAt     string         `json:"startedAt"`
	CompletedAt   string         `json:"completedAt"`
	Outcome       string         `json:"outcome"`
	Schemas       SchemaRefs     `json:"schemas"`
	Suites        []SuiteEntry   `json:"suites"`
	Files         []FileEntry    `json:"files"`
}

type SchemaRefs struct {
	Results  string `json:"results"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
	Manifest string `json:"manifest"`
}

type SuiteEntry struct {
	ID          string `json:"id"`
	CaseCount   int    `json:"caseCount"`
	InputSHA256 string `json:"inputSha256"`
}

type FileEntry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"mediaType"`
}

func ValidateRunID(value string) error {
	if !runIDPattern.MatchString(value) {
		return fmt.Errorf("run-id 必须匹配 %s", runIDPattern.String())
	}
	return nil
}

func (e Evidence) Validate() error {
	if e.SchemaVersion != model.SchemaVersionV1 {
		return fmt.Errorf("schemaVersion 必须为 %q", model.SchemaVersionV1)
	}
	if err := ValidateRunID(e.RunID); err != nil {
		return err
	}
	if !tokenPattern.MatchString(e.CaseID) || !tokenPattern.MatchString(e.Suite) {
		return fmt.Errorf("caseId 和 suite 必须是安全 token")
	}
	if e.Platform != model.PlatformWindows && e.Platform != model.PlatformLinux {
		return fmt.Errorf("platform 不受支持：%q", e.Platform)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.CapturedAt); err != nil {
		return fmt.Errorf("capturedAt 必须是 RFC3339Nano：%w", err)
	}
	switch e.Kind {
	case EvidenceAction:
		if e.Action == nil || e.Governance != nil {
			return fmt.Errorf("action_observation 必须且只能包含 action")
		}
		return validateActionObservation(*e.Action)
	case EvidenceGovernance:
		if e.Governance == nil || e.Action != nil {
			return fmt.Errorf("governance_observation 必须且只能包含 governance")
		}
		return validateGovernanceObservation(*e.Governance)
	default:
		return fmt.Errorf("evidence kind 不受支持：%q", e.Kind)
	}
}

func validateActionObservation(value ActionObservation) error {
	if !tokenPattern.MatchString(value.Operation) || value.ActionType == "" || value.Entry == "" {
		return fmt.Errorf("action observation 缺少 operation、actionType 或 entry")
	}
	if len(value.ExpectedDecision) == 0 {
		return fmt.Errorf("action observation 缺少 expectedDecision")
	}
	if value.ActualDecision != "" && !model.IsValidDecision(value.ActualDecision) {
		return fmt.Errorf("actualDecision 不受支持：%q", value.ActualDecision)
	}
	return validateObservationLimits(value.Signals, value.FailureReason, value.DurationMS, value.UpstreamCalls)
}

func validateGovernanceObservation(value GovernanceObservation) error {
	if !tokenPattern.MatchString(value.Operation) || len(value.ExpectedDecision) == 0 {
		return fmt.Errorf("governance observation 缺少 operation 或 expectedDecision")
	}
	if value.ActualDecision != "" && !model.IsValidDecision(value.ActualDecision) {
		return fmt.Errorf("actualDecision 不受支持：%q", value.ActualDecision)
	}
	return validateObservationLimits(
		value.Signals,
		value.FailureReason,
		value.DurationMS,
		value.UpstreamCallsBeforeApproval,
	)
}

func validateObservationLimits(signals []string, failureReason string, duration float64, upstreamCalls int) error {
	if len(signals) > 512 {
		return fmt.Errorf("signals 不能超过 512 项")
	}
	if len([]byte(failureReason)) > 4096 {
		return fmt.Errorf("failureReason 不能超过 4096 字节")
	}
	if duration < 0 || upstreamCalls < 0 {
		return fmt.Errorf("duration 和 upstream calls 不能为负数")
	}
	return nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != model.SchemaVersionV1 {
		return fmt.Errorf("schemaVersion 必须为 %q", model.SchemaVersionV1)
	}
	if err := ValidateRunID(m.RunID); err != nil {
		return err
	}
	if m.Platform != model.PlatformWindows && m.Platform != model.PlatformLinux {
		return fmt.Errorf("platform 不受支持：%q", m.Platform)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, m.StartedAt)
	if err != nil {
		return fmt.Errorf("startedAt 必须是 RFC3339Nano：%w", err)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, m.CompletedAt)
	if err != nil || completedAt.Before(startedAt) {
		return fmt.Errorf("completedAt 无效或早于 startedAt")
	}
	if m.Outcome != OutcomePassed && m.Outcome != OutcomeFailed {
		return fmt.Errorf("outcome 不受支持：%q", m.Outcome)
	}
	wantSchemas := SchemaRefs{
		Results:  "evaluation/schema/results.schema.json",
		Result:   "evaluation/schema/result.schema.json",
		Evidence: "evaluation/schema/evidence.schema.json",
		Manifest: "evaluation/schema/run-manifest.schema.json",
	}
	if m.Schemas != wantSchemas {
		return fmt.Errorf("schemas 与 v1 发布契约不一致")
	}
	if len(m.Suites) == 0 || len(m.Files) == 0 {
		return fmt.Errorf("manifest 必须包含 suites 和 files")
	}
	if !sort.SliceIsSorted(m.Suites, func(i, j int) bool { return m.Suites[i].ID < m.Suites[j].ID }) {
		return fmt.Errorf("suites 必须按 id 排序")
	}
	seenSuites := make(map[string]struct{}, len(m.Suites))
	for _, suite := range m.Suites {
		if !tokenPattern.MatchString(suite.ID) || suite.CaseCount < 1 || !sha256Pattern.MatchString(suite.InputSHA256) {
			return fmt.Errorf("suite entry 无效：%q", suite.ID)
		}
		if _, exists := seenSuites[suite.ID]; exists {
			return fmt.Errorf("suite 不能重复：%q", suite.ID)
		}
		seenSuites[suite.ID] = struct{}{}
	}
	if !sort.SliceIsSorted(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path }) {
		return fmt.Errorf("files 必须按 path 排序")
	}
	seenFiles := make(map[string]struct{}, len(m.Files))
	for _, file := range m.Files {
		wantMediaType, err := manifestMediaType(file.Path)
		if err != nil {
			return err
		}
		if file.Path == ManifestFileName {
			return fmt.Errorf("manifest 不得登记自身哈希")
		}
		if file.SizeBytes < 1 || !sha256Pattern.MatchString(file.SHA256) || file.MediaType != wantMediaType {
			return fmt.Errorf("file entry 无效：%q", file.Path)
		}
		if _, exists := seenFiles[file.Path]; exists {
			return fmt.Errorf("file 不能重复：%q", file.Path)
		}
		seenFiles[file.Path] = struct{}{}
	}
	for _, required := range []string{ResultsFileName, JUnitFileName, SummaryFileName, HTMLFileName} {
		if _, exists := seenFiles[required]; !exists {
			return fmt.Errorf("manifest 缺少必需文件：%s", required)
		}
	}
	return nil
}

func manifestMediaType(path string) (string, error) {
	switch path {
	case ResultsFileName:
		return mediaTypeJSON, nil
	case JUnitFileName:
		return mediaTypeXML, nil
	case SummaryFileName:
		return mediaTypeMarkdown, nil
	case HTMLFileName:
		return mediaTypeHTML, nil
	}
	if strings.HasPrefix(path, EvidenceDirName+"/") &&
		strings.HasSuffix(path, ".json") &&
		!strings.Contains(path, "\\") &&
		!strings.Contains(path, "../") {
		return mediaTypeJSON, nil
	}
	return "", fmt.Errorf("manifest 文件路径不受支持：%q", path)
}

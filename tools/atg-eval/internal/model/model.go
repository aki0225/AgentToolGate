package model

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

const SchemaVersionV1 = "v1"

type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

type Entry string

const (
	EntryGuardCore  Entry = "guard_core"
	EntryClaudeHook Entry = "claude_hook"
	EntryCodexHook  Entry = "codex_hook"
	EntryMCPInbound Entry = "mcp_inbound"
	EntryGovernance Entry = "governance"
)

type Mode string

const (
	ModeDryRun  Mode = "dry_run"
	ModeLive    Mode = "live"
	ModeOffline Mode = "offline"
)

type ActionType string

const (
	ActionCommand    ActionType = "command"
	ActionRead       ActionType = "read"
	ActionWrite      ActionType = "write"
	ActionDelete     ActionType = "delete"
	ActionNetwork    ActionType = "network"
	ActionToolCall   ActionType = "tool_call"
	ActionGovernance ActionType = "governance"
)

type Decision string

const (
	DecisionAllow            Decision = "allow"
	DecisionAsk              Decision = "ask"
	DecisionDeny             Decision = "deny"
	DecisionApprovalRequired Decision = "approval_required"
	DecisionDenyWithTicket   Decision = "deny_with_ticket"
)

type SideEffectExpectation string

const (
	SideEffectPrevented     SideEffectExpectation = "prevented"
	SideEffectAllowed       SideEffectExpectation = "allowed"
	SideEffectUnchanged     SideEffectExpectation = "unchanged"
	SideEffectNotApplicable SideEffectExpectation = "not_applicable"
)

type ResultStatus string

const (
	ResultPassed  ResultStatus = "passed"
	ResultFailed  ResultStatus = "failed"
	ResultSkipped ResultStatus = "skipped"
)

// Case 描述一个声明式评估输入。Runner 只能按 Operation 选择受限动作，
// 不能把用例字段拼成任意 shell 命令执行。
type Case struct {
	SchemaVersion string     `json:"schemaVersion"`
	ID            string     `json:"id"`
	Suite         string     `json:"suite"`
	Title         string     `json:"title"`
	Category      string     `json:"category"`
	Platforms     []Platform `json:"platforms"`
	Entry         Entry      `json:"entry"`
	Mode          Mode       `json:"mode"`
	Action        Action     `json:"action"`
	Expected      Expected   `json:"expected"`
	Tags          []string   `json:"tags,omitempty"`
}

type Action struct {
	Type      ActionType     `json:"type"`
	Operation string         `json:"operation"`
	Target    string         `json:"target,omitempty"`
	Method    string         `json:"method,omitempty"`
	URL       string         `json:"url,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type Expected struct {
	Decisions  []Decision            `json:"decision"`
	SideEffect SideEffectExpectation `json:"sideEffect"`
}

type EvidenceRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type Result struct {
	SchemaVersion               string        `json:"schemaVersion"`
	RunID                       string        `json:"runId"`
	CaseID                      string        `json:"caseId"`
	Suite                       string        `json:"suite"`
	Status                      ResultStatus  `json:"status"`
	ExpectedDecision            []Decision    `json:"expectedDecision"`
	ActualDecision              Decision      `json:"actualDecision,omitempty"`
	DurationMS                  float64       `json:"durationMs"`
	SideEffectAttempted         bool          `json:"sideEffectAttempted"`
	SideEffectObserved          bool          `json:"sideEffectObserved"`
	UpstreamCallsBeforeApproval int           `json:"upstreamCallsBeforeApproval"`
	SecretLeakDetected          bool          `json:"secretLeakDetected"`
	Evidence                    []EvidenceRef `json:"evidence"`
	SkipReason                  string        `json:"skipReason,omitempty"`
	FailureReason               string        `json:"failureReason,omitempty"`
}

var (
	tokenPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	inlineCredentialPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+\S+`)
	credentialDSNPattern    = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^:/@\s]+:[^@/\s]+@`)
	sha256Pattern           = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func (c Case) Validate() error {
	if c.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("schemaVersion 必须为 %q", SchemaVersionV1)
	}
	if err := validateToken("id", c.ID); err != nil {
		return err
	}
	if err := validateToken("suite", c.Suite); err != nil {
		return err
	}
	if strings.TrimSpace(c.Title) == "" {
		return fmt.Errorf("title 不能为空")
	}
	if len([]rune(c.Title)) > 160 {
		return fmt.Errorf("title 不能超过 160 个字符")
	}
	if err := validateToken("category", c.Category); err != nil {
		return err
	}
	if err := validatePlatforms(c.Platforms); err != nil {
		return err
	}
	if !validEntry(c.Entry) {
		return fmt.Errorf("entry 不受支持：%q", c.Entry)
	}
	if !validMode(c.Mode) {
		return fmt.Errorf("mode 不受支持：%q", c.Mode)
	}
	if err := c.Action.Validate(); err != nil {
		return fmt.Errorf("action 无效：%w", err)
	}
	if err := c.Expected.Validate(); err != nil {
		return fmt.Errorf("expected 无效：%w", err)
	}
	for _, tag := range c.Tags {
		if err := validateToken("tag", tag); err != nil {
			return err
		}
	}
	return nil
}

func (a Action) Validate() error {
	if !validActionType(a.Type) {
		return fmt.Errorf("type 不受支持：%q", a.Type)
	}
	if err := validateToken("operation", a.Operation); err != nil {
		return err
	}
	if len(a.Target) > 1024 || len(a.URL) > 2048 || len(a.Tool) > 256 {
		return fmt.Errorf("target、url 或 tool 超过长度限制")
	}
	if containsAbsolutePath(a.Target) {
		return fmt.Errorf("target 不能包含真实绝对路径，请使用 <sandbox> 占位符")
	}
	if a.Type == ActionNetwork && strings.TrimSpace(a.URL) == "" {
		return fmt.Errorf("network 动作必须提供 url")
	}
	if a.Type == ActionToolCall && strings.TrimSpace(a.Tool) == "" {
		return fmt.Errorf("tool_call 动作必须提供 tool")
	}
	if err := validateArgumentValue("arguments", a.Arguments); err != nil {
		return err
	}
	return nil
}

func (e Expected) Validate() error {
	if len(e.Decisions) == 0 {
		return fmt.Errorf("decision 至少包含一个值")
	}
	seen := make(map[Decision]struct{}, len(e.Decisions))
	for _, decision := range e.Decisions {
		if !validDecision(decision) {
			return fmt.Errorf("decision 不受支持：%q", decision)
		}
		if _, exists := seen[decision]; exists {
			return fmt.Errorf("decision 不能重复：%q", decision)
		}
		seen[decision] = struct{}{}
	}
	if !validSideEffect(e.SideEffect) {
		return fmt.Errorf("sideEffect 不受支持：%q", e.SideEffect)
	}
	return nil
}

func (r Result) Validate() error {
	if r.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("schemaVersion 必须为 %q", SchemaVersionV1)
	}
	if err := validateToken("runId", r.RunID); err != nil {
		return err
	}
	if err := validateToken("caseId", r.CaseID); err != nil {
		return err
	}
	if err := validateToken("suite", r.Suite); err != nil {
		return err
	}
	if !validResultStatus(r.Status) {
		return fmt.Errorf("status 不受支持：%q", r.Status)
	}
	if len(r.ExpectedDecision) == 0 {
		return fmt.Errorf("expectedDecision 至少包含一个值")
	}
	seen := make(map[Decision]struct{}, len(r.ExpectedDecision))
	for _, decision := range r.ExpectedDecision {
		if !validDecision(decision) {
			return fmt.Errorf("expectedDecision 不受支持：%q", decision)
		}
		if _, exists := seen[decision]; exists {
			return fmt.Errorf("expectedDecision 不能重复：%q", decision)
		}
		seen[decision] = struct{}{}
	}
	if r.Status != ResultSkipped && !validDecision(r.ActualDecision) {
		return fmt.Errorf("passed/failed 结果必须包含有效 actualDecision")
	}
	if r.ActualDecision != "" && !validDecision(r.ActualDecision) {
		return fmt.Errorf("actualDecision 不受支持：%q", r.ActualDecision)
	}
	if math.IsNaN(r.DurationMS) || math.IsInf(r.DurationMS, 0) || r.DurationMS < 0 {
		return fmt.Errorf("durationMs 必须是非负有限数")
	}
	if r.UpstreamCallsBeforeApproval < 0 {
		return fmt.Errorf("upstreamCallsBeforeApproval 不能为负数")
	}
	if r.Status == ResultSkipped && strings.TrimSpace(r.SkipReason) == "" {
		return fmt.Errorf("skipped 结果必须包含 skipReason")
	}
	if r.Status == ResultFailed && strings.TrimSpace(r.FailureReason) == "" {
		return fmt.Errorf("failed 结果必须包含 failureReason")
	}
	for index, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidence[%d] 无效：%w", index, err)
		}
	}
	return nil
}

func (e EvidenceRef) Validate() error {
	if err := validateToken("kind", e.Kind); err != nil {
		return err
	}
	path := strings.TrimSpace(e.Path)
	if path == "" {
		return fmt.Errorf("path 不能为空")
	}
	if containsAbsolutePath(path) || path == ".." ||
		strings.HasPrefix(path, "../") || strings.HasPrefix(path, `..\`) {
		return fmt.Errorf("path 必须是 run root 内的相对路径")
	}
	if e.SHA256 != "" && !sha256Pattern.MatchString(e.SHA256) {
		return fmt.Errorf("sha256 必须是 64 位小写十六进制")
	}
	return nil
}

func validateToken(field, value string) error {
	if !tokenPattern.MatchString(value) {
		return fmt.Errorf("%s 必须匹配 %s", field, tokenPattern.String())
	}
	return nil
}

func validatePlatforms(platforms []Platform) error {
	if len(platforms) == 0 {
		return fmt.Errorf("platforms 至少包含一个平台")
	}
	seen := make(map[Platform]struct{}, len(platforms))
	for _, platform := range platforms {
		if platform != PlatformWindows && platform != PlatformLinux {
			return fmt.Errorf("platform 不受支持：%q", platform)
		}
		if _, exists := seen[platform]; exists {
			return fmt.Errorf("platform 不能重复：%q", platform)
		}
		seen[platform] = struct{}{}
	}
	return nil
}

func containsAbsolutePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.HasPrefix(trimmed, "<sandbox>") {
		return false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\\`) {
		return true
	}
	return len(trimmed) >= 3 &&
		((trimmed[0] >= 'A' && trimmed[0] <= 'Z') || (trimmed[0] >= 'a' && trimmed[0] <= 'z')) &&
		trimmed[1] == ':' &&
		(trimmed[2] == '\\' || trimmed[2] == '/')
}

func validateArgumentValue(path string, value any) error {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]any:
		for key, item := range typed {
			if sensitiveArgumentKey(key) {
				return fmt.Errorf("%s.%s 不能保存凭据字段，synthetic credential 必须由 Runner 生成", path, key)
			}
			if err := validateArgumentValue(path+"."+key, item); err != nil {
				return err
			}
		}
	case []any:
		for index, item := range typed {
			if err := validateArgumentValue(fmt.Sprintf("%s[%d]", path, index), item); err != nil {
				return err
			}
		}
	case string:
		if containsAbsolutePath(typed) {
			return fmt.Errorf("%s 不能包含真实绝对路径", path)
		}
		if inlineCredentialPattern.MatchString(typed) || credentialDSNPattern.MatchString(typed) {
			return fmt.Errorf("%s 不能包含内联凭据", path)
		}
	}
	return nil
}

func sensitiveArgumentKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		default:
			return r
		}
	}, strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "authorization", "cookie", "token", "accesstoken", "refreshtoken",
		"apikey", "secret", "password", "passwd", "privatekey", "clientsecret",
		"dsn", "databaseurl", "signature":
		return true
	}
	return strings.HasSuffix(normalized, "token") ||
		strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "privatekey") ||
		strings.HasSuffix(normalized, "clientsecret")
}

func validEntry(value Entry) bool {
	switch value {
	case EntryGuardCore, EntryClaudeHook, EntryCodexHook, EntryMCPInbound, EntryGovernance:
		return true
	default:
		return false
	}
}

func validMode(value Mode) bool {
	switch value {
	case ModeDryRun, ModeLive, ModeOffline:
		return true
	default:
		return false
	}
}

func validActionType(value ActionType) bool {
	switch value {
	case ActionCommand, ActionRead, ActionWrite, ActionDelete, ActionNetwork, ActionToolCall, ActionGovernance:
		return true
	default:
		return false
	}
}

func validDecision(value Decision) bool {
	switch value {
	case DecisionAllow, DecisionAsk, DecisionDeny, DecisionApprovalRequired, DecisionDenyWithTicket:
		return true
	default:
		return false
	}
}

func validSideEffect(value SideEffectExpectation) bool {
	switch value {
	case SideEffectPrevented, SideEffectAllowed, SideEffectUnchanged, SideEffectNotApplicable:
		return true
	default:
		return false
	}
}

func validResultStatus(value ResultStatus) bool {
	switch value {
	case ResultPassed, ResultFailed, ResultSkipped:
		return true
	default:
		return false
	}
}

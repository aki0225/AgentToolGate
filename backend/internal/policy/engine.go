package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Effect string

const (
	EffectAllow           Effect = "allow"
	EffectDeny            Effect = "deny"
	EffectRequireApproval Effect = "require_approval"
)

type RuleFile struct {
	Rules []Rule `yaml:"rules" json:"rules"`
}

type Rule struct {
	Name       string     `yaml:"name" json:"name"`
	Priority   int        `yaml:"priority" json:"priority"`
	Match      Match      `yaml:"match" json:"match"`
	Conditions Conditions `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	Effect     Effect     `yaml:"effect" json:"effect"`
	Reason     string     `yaml:"reason,omitempty" json:"reason,omitempty"`
}

type Match struct {
	ToolNamespace    string `yaml:"tool_namespace,omitempty" json:"toolNamespace,omitempty"`
	ToolName         string `yaml:"tool_name,omitempty" json:"toolName,omitempty"`
	OperationType    string `yaml:"operation_type,omitempty" json:"operationType,omitempty"`
	UserRole         string `yaml:"user_role,omitempty" json:"userRole,omitempty"`
	RiskLevel        string `yaml:"risk_level,omitempty" json:"riskLevel,omitempty"`
	ActionType       string `yaml:"action_type,omitempty" json:"actionType,omitempty"`
	TargetCategory   string `yaml:"target_category,omitempty" json:"targetCategory,omitempty"`
	ContentSensitive *bool  `yaml:"content_sensitive,omitempty" json:"contentSensitive,omitempty"`
	RequiresApproval *bool  `yaml:"requires_approval,omitempty" json:"requiresApproval,omitempty"`
	ToolEnabled      *bool  `yaml:"tool_enabled,omitempty" json:"toolEnabled,omitempty"`
	SupportedTool    *bool  `yaml:"supported_tool,omitempty" json:"supportedTool,omitempty"`
}

type Conditions struct {
	TimeWindow TimeWindowCondition `yaml:"time_window,omitempty" json:"timeWindow,omitempty"`
}

type TimeWindowCondition struct {
	DenyHours []string `yaml:"deny_hours,omitempty" json:"denyHours,omitempty"`
}

type Input struct {
	ToolNamespace    string
	ToolName         string
	OperationType    string
	UserRole         string
	RiskLevel        string
	ActionType       string
	TargetCategory   string
	ContentSensitive bool
	RequiresApproval bool
	ToolEnabled      bool
	SupportedTool    bool
	Now              time.Time
}

type Decision struct {
	Effect   Effect
	Reason   string
	RuleName string
	Priority int
}

type Engine struct {
	mu      sync.RWMutex
	rules   []Rule
	path    string
	modTime time.Time
}

func LoadFile(path string) (*Engine, error) {
	rules, modTime, err := loadRulesFromFile(path)
	if err != nil {
		return nil, err
	}
	return &Engine{rules: rules, path: path, modTime: modTime}, nil
}

func NewEngine(rules []Rule) (*Engine, error) {
	normalized, err := normalizeRules(rules)
	if err != nil {
		return nil, err
	}
	return &Engine{rules: normalized}, nil
}

func NewDefaultEngine() *Engine {
	engine, err := NewEngine(DefaultRules())
	if err != nil {
		panic(err)
	}
	return engine
}

func (e *Engine) Evaluate(input Input) Decision {
	if input.Now.IsZero() {
		input.Now = time.Now()
	}

	e.mu.RLock()
	rules := cloneRules(e.rules)
	e.mu.RUnlock()

	for _, rule := range rules {
		if !matchesRule(rule, input) {
			continue
		}
		if !matchesConditions(rule.Conditions, input.Now) {
			continue
		}
		return Decision{
			Effect:   rule.Effect,
			Reason:   rule.Reason,
			RuleName: rule.Name,
			Priority: rule.Priority,
		}
	}

	return Decision{
		Effect:   EffectDeny,
		Reason:   "no policy rule matched",
		RuleName: "default-deny",
	}
}

func (e *Engine) Rules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneRules(e.rules)
}

func (e *Engine) Reload() error {
	e.mu.RLock()
	path := e.path
	e.mu.RUnlock()
	if strings.TrimSpace(path) == "" {
		return errors.New("policy engine has no file path")
	}

	rules, modTime, err := loadRulesFromFile(path)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.rules = rules
	e.modTime = modTime
	e.mu.Unlock()
	return nil
}

func (e *Engine) ReloadIfChanged() (bool, error) {
	e.mu.RLock()
	path := e.path
	lastMod := e.modTime
	e.mu.RUnlock()
	if strings.TrimSpace(path) == "" {
		return false, errors.New("policy engine has no file path")
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.ModTime().After(lastMod) {
		return false, nil
	}
	if err := e.Reload(); err != nil {
		return false, err
	}
	return true, nil
}

func (e *Engine) StartAutoReload(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = e.ReloadIfChanged()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func loadRulesFromFile(path string) ([]Rule, time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}

	var file RuleFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, time.Time{}, fmt.Errorf("parse policy file: %w", err)
	}
	rules, err := normalizeRules(file.Rules)
	if err != nil {
		return nil, time.Time{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	return rules, info.ModTime(), nil
}

func normalizeRules(rules []Rule) ([]Rule, error) {
	normalized := cloneRules(rules)
	for i := range normalized {
		normalized[i].Name = strings.TrimSpace(normalized[i].Name)
		if normalized[i].Name == "" {
			return nil, fmt.Errorf("policy rule at index %d is missing name", i)
		}
		normalized[i].Reason = strings.TrimSpace(normalized[i].Reason)
		switch normalized[i].Effect {
		case EffectAllow, EffectDeny, EffectRequireApproval:
		default:
			return nil, fmt.Errorf("policy rule %q has invalid effect %q", normalized[i].Name, normalized[i].Effect)
		}
		for _, window := range normalized[i].Conditions.TimeWindow.DenyHours {
			if _, _, err := parseHourWindow(window); err != nil {
				return nil, fmt.Errorf("policy rule %q has invalid deny_hours window %q: %w", normalized[i].Name, window, err)
			}
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Priority == normalized[j].Priority {
			return normalized[i].Name < normalized[j].Name
		}
		return normalized[i].Priority > normalized[j].Priority
	})
	return normalized, nil
}

func matchesRule(rule Rule, input Input) bool {
	match := rule.Match
	return matchString(match.ToolNamespace, input.ToolNamespace) &&
		matchString(match.ToolName, input.ToolName) &&
		matchString(match.OperationType, input.OperationType) &&
		matchString(match.UserRole, input.UserRole) &&
		matchString(match.RiskLevel, input.RiskLevel) &&
		matchString(match.ActionType, input.ActionType) &&
		matchString(match.TargetCategory, input.TargetCategory) &&
		matchBool(match.ContentSensitive, input.ContentSensitive) &&
		matchBool(match.RequiresApproval, input.RequiresApproval) &&
		matchBool(match.ToolEnabled, input.ToolEnabled) &&
		matchBool(match.SupportedTool, input.SupportedTool)
}

func matchesConditions(conditions Conditions, now time.Time) bool {
	denyHours := conditions.TimeWindow.DenyHours
	if len(denyHours) == 0 {
		return true
	}
	currentMinute := now.Hour()*60 + now.Minute()
	for _, window := range denyHours {
		start, end, err := parseHourWindow(window)
		if err != nil {
			return false
		}
		if minuteInWindow(currentMinute, start, end) {
			return true
		}
	}
	return false
}

func matchString(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" || pattern == "*" {
		return true
	}
	return pattern == strings.ToLower(strings.TrimSpace(value))
}

func matchBool(pattern *bool, value bool) bool {
	if pattern == nil {
		return true
	}
	return *pattern == value
}

func parseHourWindow(raw string) (int, int, error) {
	startRaw, endRaw, ok := strings.Cut(strings.TrimSpace(raw), "-")
	if !ok {
		return 0, 0, errors.New("expected HH:MM-HH:MM")
	}
	start, err := parseClockMinute(startRaw)
	if err != nil {
		return 0, 0, err
	}
	end, err := parseClockMinute(endRaw)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func parseClockMinute(raw string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func minuteInWindow(minute, start, end int) bool {
	if start == end {
		return true
	}
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func cloneRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i, rule := range rules {
		out[i] = cloneRule(rule)
	}
	return out
}

func cloneRule(rule Rule) Rule {
	rule.Conditions.TimeWindow.DenyHours = append([]string(nil), rule.Conditions.TimeWindow.DenyHours...)
	rule.Match.ContentSensitive = cloneBool(rule.Match.ContentSensitive)
	rule.Match.RequiresApproval = cloneBool(rule.Match.RequiresApproval)
	rule.Match.ToolEnabled = cloneBool(rule.Match.ToolEnabled)
	rule.Match.SupportedTool = cloneBool(rule.Match.SupportedTool)
	return rule
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

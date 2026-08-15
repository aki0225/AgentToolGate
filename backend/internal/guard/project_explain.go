package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type ProjectExplanation struct {
	NormalizedTargets []ProjectExplanationTarget  `json:"normalizedTargets"`
	BuiltIn           ProjectDecisionExplanation  `json:"builtIn"`
	MatchedRules      []ProjectRuleMatch          `json:"matchedRules"`
	Floor             *ProjectDecisionExplanation `json:"floor"`
	Final             ProjectDecisionExplanation  `json:"final"`
}

type ProjectDecisionExplanation struct {
	Decision  string `json:"decision"`
	RiskLevel string `json:"riskLevel"`
	Silent    bool   `json:"silent"`
	Category  string `json:"category"`
}

type ProjectExplanationTarget struct {
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Operation string `json:"operation,omitempty"`
}

type ProjectRuleMatch struct {
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Operation string `json:"operation,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
	Effect    string `json:"effect"`
}

func DecodeExplanationInput(client string, data []byte) (ActionInput, error) {
	switch lowerTrim(client) {
	case "action":
		return DecodeActionInput(data)
	case "codex":
		if err := validateStrictJSONDocument(data, "hook payload JSON"); err != nil {
			return ActionInput{}, err
		}
		input, err := AdaptCodexPayload(data)
		if err != nil {
			return ActionInput{}, err
		}
		if err := ValidateActionForExplanation(input); err != nil {
			return ActionInput{}, err
		}
		return input, nil
	case "claude":
		if err := validateStrictJSONDocument(data, "hook payload JSON"); err != nil {
			return ActionInput{}, err
		}
		input, err := AdaptClaudePayload(data)
		if err != nil {
			return ActionInput{}, err
		}
		if err := ValidateActionForExplanation(input); err != nil {
			return ActionInput{}, err
		}
		return input, nil
	default:
		return ActionInput{}, errors.New("guard explain 输入类型仅支持 codex、claude 或 action")
	}
}

func DecodeActionInput(data []byte) (ActionInput, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return ActionInput{}, errors.New("action 输入不能为空")
	}
	if err := validateStrictJSONDocument(data, "action 输入 JSON"); err != nil {
		return ActionInput{}, err
	}
	if err := validateActionInputFieldNames(data); err != nil {
		return ActionInput{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input ActionInput
	if err := decoder.Decode(&input); err != nil {
		return ActionInput{}, errors.New("action 输入 JSON 无效")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ActionInput{}, errors.New("action 输入 JSON 只能包含一个文档")
	}
	if err := ValidateActionForExplanation(input); err != nil {
		return ActionInput{}, err
	}
	return input, nil
}

func validateActionInputFieldNames(data []byte) error {
	object, ok := projectProtectionJSONObject(data)
	if !ok {
		return nil
	}
	allowed := map[string]struct{}{
		"client":         {},
		"toolName":       {},
		"actionType":     {},
		"cwd":            {},
		"projectRoot":    {},
		"command":        {},
		"target":         {},
		"contentPreview": {},
		"networkMethod":  {},
		"networkUrl":     {},
		"targets":        {},
	}
	for name := range object {
		if _, exists := allowed[name]; !exists {
			return fmt.Errorf("action 输入 JSON 包含未知字段 %q", name)
		}
	}
	return nil
}

func ValidateActionForExplanation(input ActionInput) error {
	if strings.TrimSpace(input.ToolName) == "" {
		return errors.New("action 输入 toolName 缺失")
	}
	switch lowerTrim(input.ActionType) {
	case "read", "write", "delete", "exec", "command", "execute", "network", "patch",
		"create", "update", "post", "get", "inspect", "view", "list":
		return nil
	default:
		return errors.New("action 输入 actionType 未知")
	}
}

func ExplainWithProjectProtection(input ActionInput, protection ProjectProtection) ProjectExplanation {
	builtIn := Evaluate(input)
	floor, matches := evaluateProjectProtectionMatches(input, protection)
	final := builtIn
	var floorPointer *ProjectDecisionExplanation
	if len(matches) > 0 {
		final = stricterGuardDecision(builtIn, floor)
		floorCopy := projectDecisionExplanation(floor)
		floorPointer = &floorCopy
	}
	return ProjectExplanation{
		NormalizedTargets: explainProjectTargets(input),
		BuiltIn:           projectDecisionExplanation(builtIn),
		MatchedRules:      matches,
		Floor:             floorPointer,
		Final:             projectDecisionExplanation(final),
	}
}

func projectDecisionExplanation(decision Decision) ProjectDecisionExplanation {
	return ProjectDecisionExplanation{
		Decision:  decision.Decision,
		RiskLevel: decision.RiskLevel,
		Silent:    decision.Silent,
		Category:  decision.Category,
	}
}

func evaluateProjectProtectionMatches(input ActionInput, protection ProjectProtection) (Decision, []ProjectRuleMatch) {
	if !protection.Enabled {
		return Decision{}, nil
	}

	var result Decision
	var matches []ProjectRuleMatch
	for _, targetOperation := range projectProtectionTargetOperations(input) {
		relative, ok := protectedRepoRelativePath(input, targetOperation.Target)
		if !ok {
			continue
		}
		for _, rule := range protection.ProtectedPaths {
			if !projectPathPatternMatches(rule.Pattern, relative) {
				continue
			}
			effect := protectedPathEffect(rule, targetOperation.Operation)
			if effect == "" {
				continue
			}
			candidate := projectProtectionDecision(
				effect,
				firstNonEmpty(rule.Reason, "命中项目受保护路径"),
				"project_protected_path",
			)
			result = appendProjectFloor(result, candidate, len(matches) > 0)
			matches = append(matches, ProjectRuleMatch{
				Kind:      "protected_path",
				Target:    relative,
				Operation: targetOperation.Operation,
				Pattern:   rule.Pattern,
				Effect:    effect,
			})
		}
	}

	if protection.Egress.Enabled {
		for _, networkURL := range projectNetworkWriteURLs(input) {
			host := projectNetworkHost(networkURL)
			if host == "" || projectHostAllowed(host, protection.Egress.AllowedHosts) {
				continue
			}
			candidate := projectProtectionDecision(
				protection.Egress.UnlistedWrite,
				"项目外发规则要求确认",
				"project_egress",
			)
			result = appendProjectFloor(result, candidate, len(matches) > 0)
			matches = append(matches, ProjectRuleMatch{
				Kind:      "egress",
				Target:    host,
				Operation: "write",
				Effect:    protection.Egress.UnlistedWrite,
			})
		}
	}
	return result, matches
}

func appendProjectFloor(current, candidate Decision, matched bool) Decision {
	if !matched {
		return candidate
	}
	return stricterGuardDecision(current, candidate)
}

func explainProjectTargets(input ActionInput) []ProjectExplanationTarget {
	var targets []ProjectExplanationTarget
	for _, operation := range projectProtectionTargetOperations(input) {
		value := "<outside-repo>"
		if relative, ok := protectedRepoRelativePath(input, operation.Target); ok {
			value = relative
		}
		targets = appendExplanationTarget(targets, ProjectExplanationTarget{
			Kind:      "path",
			Value:     value,
			Operation: operation.Operation,
		})
	}
	for _, rawURL := range projectNetworkWriteURLs(input) {
		host := projectNetworkHost(rawURL)
		if host == "" {
			continue
		}
		targets = appendExplanationTarget(targets, ProjectExplanationTarget{
			Kind:      "network",
			Value:     host,
			Operation: "write",
		})
	}
	if len(targets) == 0 {
		target := strings.TrimSpace(input.Target)
		if target != "" {
			if parsed, err := url.Parse(target); err == nil && parsed.Hostname() != "" {
				targets = append(targets, ProjectExplanationTarget{Kind: "network", Value: projectNetworkHost(target)})
			} else {
				value := "<outside-repo>"
				if relative, ok := protectedRepoRelativePath(input, target); ok {
					value = relative
				}
				targets = append(targets, ProjectExplanationTarget{Kind: "path", Value: value})
			}
		}
	}
	return targets
}

func appendExplanationTarget(targets []ProjectExplanationTarget, candidate ProjectExplanationTarget) []ProjectExplanationTarget {
	for _, existing := range targets {
		if existing.Kind == candidate.Kind && existing.Value == candidate.Value && existing.Operation == candidate.Operation {
			return targets
		}
	}
	return append(targets, candidate)
}

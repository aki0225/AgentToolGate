package app

import (
	"os"
	"path/filepath"
	"strings"
)

type agentGuardTargetResolution struct {
	ResolvedPath         string
	ResolvedParentPath   string
	CanonicalTarget      string
	ResolvedFileIdentity string
	ParentIdentity       string
	TargetExists         bool
	TargetIdentityStable bool
}

func resolveAgentGuardTarget(normalizedTarget string) agentGuardTargetResolution {
	target := strings.TrimSpace(normalizedTarget)
	resolution := agentGuardTargetResolution{CanonicalTarget: target}
	if target == "" {
		return resolution
	}

	if resolvedPath, fileIdentity, ok := resolveAgentGuardPathIdentity(target); ok {
		resolution.ResolvedPath = resolvedPath
		resolution.ResolvedFileIdentity = strings.TrimSpace(fileIdentity)
		resolution.CanonicalTarget = canonicalAgentGuardTarget(resolvedPath, fileIdentity)
		resolution.ResolvedParentPath, resolution.ParentIdentity = resolveAgentGuardParent(resolvedPath)
		resolution.TargetExists = true
		resolution.TargetIdentityStable = true
		return resolution
	}

	if targetExists(target) {
		resolution.TargetExists = true
		resolution.TargetIdentityStable = false
		resolution.ResolvedPath = target
	}
	resolution.ResolvedParentPath, resolution.ParentIdentity = resolveAgentGuardParent(target)
	return resolution
}

func targetExists(path string) bool {
	candidate := strings.TrimSpace(path)
	if candidate == "" {
		return false
	}
	if absPath, err := filepath.Abs(candidate); err == nil && strings.TrimSpace(absPath) != "" {
		candidate = absPath
	}
	candidate = filepath.Clean(candidate)
	if candidate == "" || candidate == "." {
		return false
	}
	_, err := os.Lstat(candidate)
	return err == nil
}

func resolveAgentGuardParentIdentity(path string) string {
	_, identity := resolveAgentGuardParent(path)
	return identity
}

func resolveAgentGuardParent(path string) (string, string) {
	parent := strings.TrimSpace(filepath.Dir(path))
	if parent == "" || parent == "." {
		return "", ""
	}
	if resolvedPath, fileIdentity, ok := resolveAgentGuardPathIdentity(parent); ok {
		return resolvedPath, canonicalAgentGuardTarget(resolvedPath, fileIdentity)
	}
	normalized := normalizeAgentGuardTarget(parent)
	return normalized, normalized
}

func canonicalAgentGuardTarget(path, fileIdentity string) string {
	if strings.TrimSpace(fileIdentity) != "" {
		return "fileid:" + strings.TrimSpace(fileIdentity)
	}
	return normalizeAgentGuardTarget(path)
}

func classifyAgentGuardTargetCategory(target string) string {
	normalized := normalizeAgentGuardTarget(target)
	if normalized == "" {
		return "unknown"
	}
	if isAgentGuardSelfTamperTarget(normalized) {
		return "self_tamper"
	}
	if isAgentGuardSensitiveTarget(normalized) {
		return "sensitive"
	}
	if !isAgentGuardAbsoluteTarget(normalized) {
		return "workspace"
	}
	return "external"
}

func classifyAgentGuardTargetCategoryWithResolution(target string, resolution agentGuardTargetResolution) string {
	candidates := agentGuardTargetCategoryCandidates(target, resolution)
	for _, candidate := range candidates {
		if isAgentGuardSelfTamperTarget(normalizeAgentGuardTarget(candidate)) {
			return "self_tamper"
		}
	}
	for _, candidate := range candidates {
		if isAgentGuardSensitiveTarget(normalizeAgentGuardTarget(candidate)) {
			return "sensitive"
		}
	}
	for _, candidate := range candidates[1:] {
		normalized := normalizeAgentGuardTarget(candidate)
		if normalized != "" && isAgentGuardAbsoluteTarget(normalized) {
			return "external"
		}
	}
	return classifyAgentGuardTargetCategory(target)
}

func agentGuardTargetCategoryCandidates(target string, resolution agentGuardTargetResolution) []string {
	candidates := []string{
		target,
		resolution.ResolvedPath,
		resolution.ResolvedParentPath,
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(resolution.CanonicalTarget)), "fileid:") {
		candidates = append(candidates, resolution.CanonicalTarget)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(resolution.ParentIdentity)), "fileid:") {
		candidates = append(candidates, resolution.ParentIdentity)
	}
	return candidates
}

func isAgentGuardAbsoluteTarget(target string) bool {
	normalized := normalizeAgentGuardTarget(target)
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(normalized) || strings.HasPrefix(normalized, `\\`) {
		return true
	}
	return len(normalized) >= 3 && normalized[1] == ':' && (normalized[2] == '\\' || normalized[2] == '/')
}

func isAgentGuardSelfTamperTarget(target string) bool {
	if target == "" {
		return false
	}
	exactFiles := []string{
		`.claude/settings.json`,
		`.codex/hooks.json`,
		`configs/policies.yaml`,
		`agenttoolgate.exe`,
	}
	for _, exactFile := range exactFiles {
		if agentGuardPathMatchesExactFile(target, exactFile) {
			return true
		}
	}
	dirs := []string{
		`.claude/hooks`,
		`.codex/hooks`,
		`backend/cmd/server`,
		`cmd/server`,
	}
	for _, dir := range dirs {
		if agentGuardPathMatchesDirOrDescendant(target, dir) {
			return true
		}
	}
	return false
}

func agentGuardPathMatchesExactFile(target, file string) bool {
	return agentGuardSegmentsHaveSuffix(agentGuardPathSegments(target), agentGuardPathSegments(file))
}

func agentGuardPathMatchesDirOrDescendant(target, dir string) bool {
	return agentGuardSegmentsContainSequence(agentGuardPathSegments(target), agentGuardPathSegments(dir))
}

func agentGuardPathHasSegment(target, segment string) bool {
	return agentGuardSegmentsContainSequence(agentGuardPathSegments(target), agentGuardPathSegments(segment))
}

func agentGuardPathSegments(target string) []string {
	normalized := strings.ToLower(strings.TrimSpace(target))
	if normalized == "" {
		return nil
	}
	normalized = strings.ReplaceAll(normalized, "/", `\`)
	for strings.HasPrefix(normalized, `\\?\`) {
		normalized = strings.TrimPrefix(normalized, `\\?\`)
	}
	normalized = strings.Trim(normalized, `\`)
	rawParts := strings.Split(normalized, `\`)
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimRight(strings.TrimSpace(part), " .")
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

func agentGuardSegmentsHaveSuffix(segments, suffix []string) bool {
	if len(suffix) == 0 || len(segments) < len(suffix) {
		return false
	}
	offset := len(segments) - len(suffix)
	for i := range suffix {
		if segments[offset+i] != suffix[i] {
			return false
		}
	}
	return true
}

func agentGuardSegmentsContainSequence(segments, sequence []string) bool {
	if len(sequence) == 0 || len(segments) < len(sequence) {
		return false
	}
	for offset := 0; offset <= len(segments)-len(sequence); offset++ {
		matched := true
		for i := range sequence {
			if segments[offset+i] != sequence[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

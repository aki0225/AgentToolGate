package app

import (
	"os"
	"path"
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

func (a *App) resolveAgentGuardTargetWithinWorkspace(target, workspaceRoot string) agentGuardTargetResolution {
	return a.resolveAgentGuardTargetWithinContext(target, workspaceRoot, "")
}

func (a *App) trustedAgentGuardWorkspaceRoot(claimedRoot string) string {
	trustedRoot := normalizeAgentGuardTarget(a.cfg.ProjectRoot)
	if trustedRoot == "" {
		return ""
	}
	claimed := normalizeAgentGuardTarget(claimedRoot)
	if claimed == "" || agentGuardPathsEqual(claimed, trustedRoot) {
		return trustedRoot
	}
	return ""
}

func (a *App) resolveAgentGuardTargetWithinContext(target, workspaceRoot, workingDirectory string) agentGuardTargetResolution {
	candidate := strings.TrimSpace(target)
	root := strings.TrimSpace(workspaceRoot)
	base := strings.TrimSpace(workingDirectory)
	if base == "" {
		base = root
	} else if root != "" && !isAgentGuardAbsoluteTarget(base) {
		base = normalizeAgentGuardTarget(filepath.Join(root, base))
	}
	if candidate != "" && base != "" && !isAgentGuardAbsoluteTarget(candidate) {
		candidate = normalizeAgentGuardTarget(filepath.Join(base, candidate))
	}
	return a.resolveAgentGuardTarget(candidate)
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
	return classifyAgentGuardTargetCategoryWithWorkspace(target, "", resolution)
}

func classifyAgentGuardTargetCategoryWithWorkspace(target, workspaceRoot string, resolution agentGuardTargetResolution) string {
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
	if root := normalizeAgentGuardTarget(workspaceRoot); root != "" {
		hasResolvedPath := false
		for _, candidate := range []string{target, resolution.ResolvedPath, resolution.ResolvedParentPath} {
			normalized := normalizeAgentGuardTarget(candidate)
			if normalized == "" || !isAgentGuardAbsoluteTarget(normalized) {
				continue
			}
			hasResolvedPath = true
			if !agentGuardPathWithinRoot(normalized, root) {
				return "external"
			}
		}
		if hasResolvedPath || !isAgentGuardAbsoluteTarget(target) {
			return "workspace"
		}
	}
	if root := normalizeAgentGuardTarget(workspaceRoot); root == "" && agentGuardRelativePathEscapesUnknownRoot(target) {
		return "external"
	}
	for _, candidate := range candidates[1:] {
		normalized := normalizeAgentGuardTarget(candidate)
		if normalized != "" && isAgentGuardAbsoluteTarget(normalized) {
			return "external"
		}
	}
	return classifyAgentGuardTargetCategory(target)
}

func agentGuardPathsEqual(left, right string) bool {
	leftPath := agentGuardComparablePath(left)
	rightPath := agentGuardComparablePath(right)
	if leftPath.windows != rightPath.windows ||
		leftPath.absolute != rightPath.absolute ||
		len(leftPath.segments) == 0 ||
		len(leftPath.segments) != len(rightPath.segments) {
		return false
	}
	for index := range leftPath.segments {
		if leftPath.segments[index] != rightPath.segments[index] {
			return false
		}
	}
	return true
}

func agentGuardRelativePathEscapesUnknownRoot(target string) bool {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" || isAgentGuardAbsoluteTarget(trimmed) {
		return false
	}
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	cleaned := path.Clean(normalized)
	return cleaned == ".." || strings.HasPrefix(cleaned, "../")
}

func agentGuardPathWithinRoot(candidate, root string) bool {
	candidatePath := agentGuardComparablePath(candidate)
	rootPath := agentGuardComparablePath(root)
	if candidatePath.windows != rootPath.windows ||
		candidatePath.absolute != rootPath.absolute ||
		len(candidatePath.segments) < len(rootPath.segments) ||
		len(rootPath.segments) == 0 {
		return false
	}
	for index := range rootPath.segments {
		if candidatePath.segments[index] != rootPath.segments[index] {
			return false
		}
	}
	return true
}

type agentGuardComparablePathParts struct {
	windows  bool
	absolute bool
	segments []string
}

func agentGuardComparablePath(target string) agentGuardComparablePathParts {
	normalized := normalizeAgentGuardTarget(target)
	if normalized == "" {
		return agentGuardComparablePathParts{}
	}

	if !strings.HasPrefix(normalized, "/") && looksLikeWindowsPath(normalized) {
		normalized = strings.ToLower(strings.ReplaceAll(normalized, "/", `\`))
		for strings.HasPrefix(normalized, `\\?\`) {
			normalized = strings.TrimPrefix(normalized, `\\?\`)
		}
		absolute := isAgentGuardAbsoluteTarget(normalized)
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
		return agentGuardComparablePathParts{windows: true, absolute: absolute, segments: parts}
	}

	cleaned := path.Clean(normalized)
	absolute := strings.HasPrefix(cleaned, "/")
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return agentGuardComparablePathParts{absolute: absolute}
	}
	return agentGuardComparablePathParts{
		absolute: absolute,
		segments: strings.Split(cleaned, "/"),
	}
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
		`.tmp/agenttoolgate/hook-control.json`,
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

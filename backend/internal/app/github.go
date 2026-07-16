package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultGitHubAPIBaseURL = "https://api.github.com"
	defaultGitHubTimeout    = 3 * time.Second
	hardGitHubTimeout       = 30 * time.Second
	maxGitHubIssueTitleLen  = 256
	maxGitHubIssueBodyLen   = 50000
	maxGitHubPullNumber     = 2147483647
)

var (
	githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

type githubRepoRef struct {
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	FullName string `json:"fullName"`
}

type githubAllowedRepos struct {
	byKey map[string]githubRepoRef
	items []githubRepoRef
}

type githubPullRequestArgs struct {
	Owner      string
	Repo       string
	PullNumber int
}

type githubCreateIssueArgs struct {
	Owner string
	Repo  string
	Title string
	Body  string
}

type githubConnectorConfig struct {
	APIBaseURL     string   `json:"apiBaseURL"`
	AllowedRepos   []string `json:"allowedRepos"`
	TokenSecretRef string   `json:"tokenSecretRef"`
}

type githubRuntimeConfig struct {
	APIBaseURL     string
	AllowedRepos   []string
	TokenSecretRef string
}

// validateToolCallBeforePolicyResponse 只做必须在审批前完成的参数校验。
// 这样写操作的坏参数不会生成无意义审批单，也不会触达外部 Connector。
func (a *App) validateToolCallBeforePolicyResponse(ctx context.Context, workspaceID string, tool model.Tool, decodedArgs any) error {
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	if namespace == "http" && name == "request" {
		return a.validateHTTPRequestBeforePolicy(ctx, workspaceID, decodedArgs)
	}
	if strings.HasPrefix(namespace, "mcp_") {
		return a.validateMCPToolCallBeforePolicy(ctx, workspaceID, tool, decodedArgs)
	}
	if namespace != "github" {
		return nil
	}

	switch name {
	case "list_repos":
		_, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		return err
	case "get_pull_request":
		runtimeCfg, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return err
		}
		_, err = a.parseGitHubPullRequestArgs(decodedArgs, runtimeCfg)
		if err != nil {
			return err
		}
		_, err = a.resolveGitHubToken(ctx, workspaceID, runtimeCfg)
		return err
	case "create_issue":
		runtimeCfg, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return err
		}
		_, err = a.parseGitHubCreateIssueArgs(decodedArgs, runtimeCfg)
		if err != nil {
			return err
		}
		_, err = a.resolveGitHubToken(ctx, workspaceID, runtimeCfg)
		return err
	default:
		return badRequest(fmt.Sprintf("github tool %s is not supported", tool.Key()))
	}
}

func (a *App) executeGitHubTool(ctx context.Context, workspaceID string, tool model.Tool, decodedArgs any) (resultPayload map[string]any, resultJSON json.RawMessage, err error) {
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	ctx, span := telemetry.StartSpan(ctx, "connector.github."+name, attribute.String("tool.key", tool.Key()))
	defer func() {
		if err != nil {
			telemetry.RecordError(span, err)
		}
		span.End()
	}()

	switch name {
	case "list_repos":
		runtimeCfg, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return nil, nil, err
		}
		span.SetAttributes(
			attribute.String("github.owner", "*"),
			attribute.String("github.repo", "*"),
			attribute.String("github.api_path", "configured_allowlist"),
		)
		return a.executeGitHubListRepos(runtimeCfg)
	case "get_pull_request":
		runtimeCfg, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return nil, nil, err
		}
		args, err := a.parseGitHubPullRequestArgs(decodedArgs, runtimeCfg)
		if err != nil {
			return nil, nil, err
		}
		path := githubPullRequestAPIPath(args)
		span.SetAttributes(
			attribute.String("github.owner", args.Owner),
			attribute.String("github.repo", args.Repo),
			attribute.String("github.api_path", path),
		)
		return a.executeGitHubGetPullRequest(ctx, workspaceID, runtimeCfg, args, path)
	case "create_issue":
		runtimeCfg, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return nil, nil, err
		}
		args, err := a.parseGitHubCreateIssueArgs(decodedArgs, runtimeCfg)
		if err != nil {
			return nil, nil, err
		}
		path := githubIssueAPIPath(args)
		span.SetAttributes(
			attribute.String("github.owner", args.Owner),
			attribute.String("github.repo", args.Repo),
			attribute.String("github.api_path", path),
		)
		return a.executeGitHubCreateIssue(ctx, workspaceID, runtimeCfg, args, path)
	default:
		return nil, nil, fmt.Errorf("tool %s is not supported in the current skeleton", tool.Key())
	}
}

func (a *App) executeGitHubListRepos(runtimeCfg githubRuntimeConfig) (map[string]any, json.RawMessage, error) {
	allowed, err := parseGitHubAllowedRepos(runtimeCfg.AllowedRepos)
	if err != nil {
		return nil, nil, err
	}
	result := map[string]any{
		"repositories": allowed.items,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}
	return result, resultJSON, nil
}

func (a *App) executeGitHubGetPullRequest(ctx context.Context, workspaceID string, runtimeCfg githubRuntimeConfig, args githubPullRequestArgs, path string) (map[string]any, json.RawMessage, error) {
	var apiResponse struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}

	if err := a.githubAPIRequest(ctx, workspaceID, runtimeCfg, http.MethodGet, path, nil, &apiResponse); err != nil {
		return nil, nil, err
	}

	result := map[string]any{
		"owner":     args.Owner,
		"repo":      args.Repo,
		"number":    apiResponse.Number,
		"title":     apiResponse.Title,
		"state":     apiResponse.State,
		"htmlUrl":   apiResponse.HTMLURL,
		"userLogin": apiResponse.User.Login,
		"headRef":   apiResponse.Head.Ref,
		"baseRef":   apiResponse.Base.Ref,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}
	return result, resultJSON, nil
}

func (a *App) executeGitHubCreateIssue(ctx context.Context, workspaceID string, runtimeCfg githubRuntimeConfig, args githubCreateIssueArgs, path string) (map[string]any, json.RawMessage, error) {
	var apiResponse struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
	}

	body := map[string]string{
		"title": args.Title,
		"body":  args.Body,
	}
	if err := a.githubAPIRequest(ctx, workspaceID, runtimeCfg, http.MethodPost, path, body, &apiResponse); err != nil {
		return nil, nil, err
	}

	result := map[string]any{
		"owner":   args.Owner,
		"repo":    args.Repo,
		"number":  apiResponse.Number,
		"title":   apiResponse.Title,
		"htmlUrl": apiResponse.HTMLURL,
		"state":   apiResponse.State,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}
	return result, resultJSON, nil
}

func githubPullRequestAPIPath(args githubPullRequestArgs) string {
	return fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(args.Owner), url.PathEscape(args.Repo), args.PullNumber)
}

func githubIssueAPIPath(args githubCreateIssueArgs) string {
	return fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(args.Owner), url.PathEscape(args.Repo))
}

func (a *App) githubAPIRequest(ctx context.Context, workspaceID string, runtimeCfg githubRuntimeConfig, method, path string, requestBody any, responseTarget any) error {
	token, err := a.resolveGitHubToken(ctx, workspaceID, runtimeCfg)
	if err != nil {
		return err
	}

	endpoint, err := buildGitHubAPIURL(runtimeCfg.APIBaseURL, path)
	if err != nil {
		return err
	}

	var bodyReader io.Reader
	if requestBody != nil {
		raw, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}

	timeout := effectiveGitHubTimeout(time.Duration(a.cfg.GitHubTimeoutMs) * time.Millisecond)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return fmt.Errorf("github API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 不读取/透传响应体，避免 mock 或上游错误体把 Authorization 等敏感信息带回审计。
		return fmt.Errorf("github API request failed with status %d", resp.StatusCode)
	}
	if responseTarget == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(responseTarget); err != nil {
		return fmt.Errorf("decode github API response: %w", err)
	}
	return nil
}

func (a *App) resolveGitHubRuntimeConfig(ctx context.Context, workspaceID string) (githubRuntimeConfig, error) {
	runtimeCfg := githubRuntimeConfig{
		APIBaseURL:   a.cfg.GitHubAPIBaseURL,
		AllowedRepos: append([]string(nil), a.cfg.GitHubAllowedRepos...),
	}

	connector, err := lookupConnectorByTypeAndName(ctx, a.store, workspaceID, "github", "default")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return runtimeCfg, nil
		}
		return githubRuntimeConfig{}, err
	}

	parsed, err := parseGitHubConnectorConfig(connector.ConfigJSON)
	if err != nil {
		return githubRuntimeConfig{}, err
	}
	if trimmed := strings.TrimSpace(parsed.APIBaseURL); trimmed != "" {
		runtimeCfg.APIBaseURL = trimmed
	}
	if len(parsed.AllowedRepos) > 0 {
		runtimeCfg.AllowedRepos = append([]string(nil), parsed.AllowedRepos...)
	}
	if trimmed := strings.TrimSpace(parsed.TokenSecretRef); trimmed != "" {
		if !isValidSecretReferenceValue(trimmed) {
			return githubRuntimeConfig{}, badRequest("github token secret ref is invalid")
		}
		runtimeCfg.TokenSecretRef = trimmed
	}
	return runtimeCfg, nil
}

func (a *App) resolveGitHubToken(ctx context.Context, workspaceID string, runtimeCfg ...githubRuntimeConfig) (string, error) {
	cfg := githubRuntimeConfig{
		APIBaseURL:   a.cfg.GitHubAPIBaseURL,
		AllowedRepos: append([]string(nil), a.cfg.GitHubAllowedRepos...),
	}
	if len(runtimeCfg) > 0 {
		cfg = runtimeCfg[0]
	} else {
		resolved, err := a.resolveGitHubRuntimeConfig(ctx, workspaceID)
		if err != nil {
			return "", err
		}
		cfg = resolved
	}

	if trimmed := strings.TrimSpace(cfg.TokenSecretRef); trimmed != "" {
		return a.resolveSecretRefValue(ctx, workspaceID, trimmed)
	}

	token := strings.TrimSpace(a.cfg.GitHubToken)
	if token == "" {
		return "", fmt.Errorf("github token is not configured")
	}
	return token, nil
}

func parseGitHubConnectorConfig(raw json.RawMessage) (githubConnectorConfig, error) {
	var cfg githubConnectorConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return githubConnectorConfig{}, badRequest("github connector config is invalid")
	}
	if cfg.APIBaseURL != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.APIBaseURL)), "http") {
		return githubConnectorConfig{}, badRequest("github API base URL is invalid")
	}
	if cfg.TokenSecretRef != "" && !isValidSecretReferenceValue(cfg.TokenSecretRef) {
		return githubConnectorConfig{}, badRequest("github token secret ref is invalid")
	}
	return cfg, nil
}

func (a *App) parseGitHubPullRequestArgs(decodedArgs any, runtimeCfg githubRuntimeConfig) (githubPullRequestArgs, error) {
	obj, err := githubArgsObject(decodedArgs)
	if err != nil {
		return githubPullRequestArgs{}, err
	}
	owner, repo, err := parseAllowedGitHubRepo(obj, runtimeCfg.AllowedRepos)
	if err != nil {
		return githubPullRequestArgs{}, err
	}
	pullNumber, err := githubPositiveIntArg(obj, "pullNumber")
	if err != nil {
		return githubPullRequestArgs{}, err
	}
	return githubPullRequestArgs{
		Owner:      owner,
		Repo:       repo,
		PullNumber: pullNumber,
	}, nil
}

func (a *App) parseGitHubCreateIssueArgs(decodedArgs any, runtimeCfg githubRuntimeConfig) (githubCreateIssueArgs, error) {
	obj, err := githubArgsObject(decodedArgs)
	if err != nil {
		return githubCreateIssueArgs{}, err
	}
	owner, repo, err := parseAllowedGitHubRepo(obj, runtimeCfg.AllowedRepos)
	if err != nil {
		return githubCreateIssueArgs{}, err
	}
	title, err := githubStringArg(obj, "title", true, maxGitHubIssueTitleLen)
	if err != nil {
		return githubCreateIssueArgs{}, err
	}
	body, err := githubStringArg(obj, "body", false, maxGitHubIssueBodyLen)
	if err != nil {
		return githubCreateIssueArgs{}, err
	}
	return githubCreateIssueArgs{
		Owner: owner,
		Repo:  repo,
		Title: title,
		Body:  body,
	}, nil
}

func parseAllowedGitHubRepo(obj map[string]any, rawAllowedRepos []string) (string, string, error) {
	owner, err := githubStringArg(obj, "owner", true, 39)
	if err != nil {
		return "", "", err
	}
	repo, err := githubStringArg(obj, "repo", true, 100)
	if err != nil {
		return "", "", err
	}
	ref, err := normalizeGitHubRepoRef(owner, repo)
	if err != nil {
		return "", "", err
	}
	allowed, err := parseGitHubAllowedRepos(rawAllowedRepos)
	if err != nil {
		return "", "", err
	}
	if !allowed.Contains(ref.Owner, ref.Repo) {
		return "", "", badRequest(fmt.Sprintf("github repository %s is not allowed", ref.FullName))
	}
	return ref.Owner, ref.Repo, nil
}

func parseGitHubAllowedRepos(rawRepos []string) (githubAllowedRepos, error) {
	result := githubAllowedRepos{
		byKey: map[string]githubRepoRef{},
		items: []githubRepoRef{},
	}
	for _, raw := range rawRepos {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		owner, repo, ok := strings.Cut(trimmed, "/")
		if !ok || strings.Contains(repo, "/") {
			return githubAllowedRepos{}, badRequest("github allowed repos must use owner/repo format")
		}
		ref, err := normalizeGitHubRepoRef(owner, repo)
		if err != nil {
			return githubAllowedRepos{}, err
		}
		key := githubRepoKey(ref.Owner, ref.Repo)
		if _, exists := result.byKey[key]; exists {
			continue
		}
		result.byKey[key] = ref
		result.items = append(result.items, ref)
	}
	if len(result.items) == 0 {
		return githubAllowedRepos{}, badRequest("github allowed repo whitelist is not configured")
	}
	sort.Slice(result.items, func(i, j int) bool {
		return result.items[i].FullName < result.items[j].FullName
	})
	return result, nil
}

func (r githubAllowedRepos) Contains(owner, repo string) bool {
	_, ok := r.byKey[githubRepoKey(owner, repo)]
	return ok
}

func normalizeGitHubRepoRef(owner, repo string) (githubRepoRef, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if !githubOwnerPattern.MatchString(owner) {
		return githubRepoRef{}, badRequest("github owner is invalid")
	}
	if !githubRepoPattern.MatchString(repo) || repo == "." || repo == ".." || strings.HasPrefix(repo, ".git") {
		return githubRepoRef{}, badRequest("github repo is invalid")
	}
	return githubRepoRef{
		Owner:    owner,
		Repo:     repo,
		FullName: owner + "/" + repo,
	}, nil
}

func githubRepoKey(owner, repo string) string {
	return strings.ToLower(strings.TrimSpace(owner)) + "/" + strings.ToLower(strings.TrimSpace(repo))
}

func githubArgsObject(decodedArgs any) (map[string]any, error) {
	if decodedArgs == nil {
		return nil, badRequest("github arguments must be a JSON object")
	}
	obj, ok := decodedArgs.(map[string]any)
	if !ok {
		return nil, badRequest("github arguments must be a JSON object")
	}
	return obj, nil
}

func githubStringArg(obj map[string]any, key string, required bool, maxLen int) (string, error) {
	value, exists := obj[key]
	if !exists || value == nil {
		if required {
			return "", badRequest(fmt.Sprintf("github %s is required", key))
		}
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", badRequest(fmt.Sprintf("github %s must be a string", key))
	}
	if key != "body" {
		text = strings.TrimSpace(text)
	}
	if required && strings.TrimSpace(text) == "" {
		return "", badRequest(fmt.Sprintf("github %s is required", key))
	}
	if maxLen > 0 && len(text) > maxLen {
		return "", badRequest(fmt.Sprintf("github %s is too long", key))
	}
	return text, nil
}

func githubPositiveIntArg(obj map[string]any, key string) (int, error) {
	value, exists := obj[key]
	if !exists || value == nil {
		return 0, badRequest(fmt.Sprintf("github %s is required", key))
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case int32:
		number = float64(typed)
	default:
		return 0, badRequest(fmt.Sprintf("github %s must be a positive integer", key))
	}
	if number <= 0 || math.Trunc(number) != number || number > float64(maxGitHubPullNumber) {
		return 0, badRequest(fmt.Sprintf("github %s must be a positive integer", key))
	}
	return int(number), nil
}

func buildGitHubAPIURL(baseURL, path string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultGitHubAPIBaseURL
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("github API base URL is invalid")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("github API base URL is invalid")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func effectiveGitHubTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultGitHubTimeout
	}
	if value > hardGitHubTimeout {
		return hardGitHubTimeout
	}
	return value
}

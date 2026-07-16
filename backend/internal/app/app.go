package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/policy"
	"agenttoolgate/backend/internal/static"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
)

type App struct {
	cfg                     config.Config
	store                   store.Store
	authenticator           *auth.Authenticator
	logger                  *slog.Logger
	policies                *policy.Engine
	approvalHub             *approvalSSEHub
	agentGuardResolveTarget func(string) agentGuardTargetResolution
	rateLimiters            sync.Map
	frontendFS              http.FileSystem
}

type requestContextKey struct{}

var errBadRequest = errors.New("bad request")
var errForbidden = errors.New("forbidden")

type RequestContext struct {
	Identity  auth.Identity   `json:"identity"`
	Workspace model.Workspace `json:"workspace"`
	User      model.User      `json:"user"`
}

type createWorkspaceRequest struct {
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	ZitadelOrganizationID string `json:"zitadelOrganizationId"`
}

type createToolRequest struct {
	Namespace        string          `json:"namespace"`
	Name             string          `json:"name"`
	DisplayName      string          `json:"displayName"`
	Description      string          `json:"description"`
	OperationType    string          `json:"operationType"`
	RiskLevel        string          `json:"riskLevel"`
	RequiresApproval bool            `json:"requiresApproval"`
	InputSchemaJSON  json.RawMessage `json:"inputSchemaJson"`
	OutputSchemaJSON json.RawMessage `json:"outputSchemaJson"`
	Enabled          *bool           `json:"enabled"`
}

type updateToolRequest struct {
	Enabled *bool `json:"enabled"`
}

type createToolCallRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type approvalReviewRequest struct {
	Reason string `json:"reason"`
}

type toolCallResponse struct {
	Status         string `json:"status"`
	Result         any    `json:"result,omitempty"`
	CallID         string `json:"callId,omitempty"`
	TraceID        string `json:"traceId,omitempty"`
	Message        string `json:"message,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ApprovalID     string `json:"approvalId,omitempty"`
	ApprovalStatus string `json:"approvalStatus,omitempty"`
}

type approvalActionResponse struct {
	Approval model.ApprovalRequest `json:"approval"`
	ToolCall model.ToolCall        `json:"toolCall"`
	Result   any                   `json:"result,omitempty"`
}

type policyRuleResponse struct {
	Name       string                   `json:"name"`
	Priority   int                      `json:"priority"`
	Match      policyMatchResponse      `json:"match"`
	Conditions policyConditionsResponse `json:"conditions,omitempty"`
	Effect     string                   `json:"effect"`
	Reason     string                   `json:"reason"`
	Enabled    bool                     `json:"enabled"`
}

type policyMatchResponse struct {
	ToolNamespace    string `json:"toolNamespace,omitempty"`
	ToolName         string `json:"toolName,omitempty"`
	OperationType    string `json:"operationType,omitempty"`
	UserRole         string `json:"userRole,omitempty"`
	RiskLevel        string `json:"riskLevel,omitempty"`
	ActionType       string `json:"actionType,omitempty"`
	TargetCategory   string `json:"targetCategory,omitempty"`
	ContentSensitive *bool  `json:"contentSensitive,omitempty"`
	RequiresApproval *bool  `json:"requiresApproval,omitempty"`
	ToolEnabled      *bool  `json:"toolEnabled,omitempty"`
	SupportedTool    *bool  `json:"supportedTool,omitempty"`
}

type policyConditionsResponse struct {
	DenyHours []string `json:"denyHours,omitempty"`
}

func New(cfg config.Config, st store.Store, authenticator *auth.Authenticator, logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{
		cfg:                     cfg,
		store:                   st,
		authenticator:           authenticator,
		logger:                  logger,
		policies:                policy.NewDefaultEngine(),
		approvalHub:             newApprovalSSEHub(logger),
		agentGuardResolveTarget: resolveAgentGuardTarget,
	}
	if frontendFS, ok := static.Frontend(); ok {
		app.frontendFS = http.FS(frontendFS)
	}
	app.reloadPolicyEngine()
	return app
}

func (a *App) HasEmbeddedFrontend() bool {
	return a != nil && a.frontendFS != nil
}

func (a *App) resolveAgentGuardTarget(raw string) agentGuardTargetResolution {
	if a != nil && a.agentGuardResolveTarget != nil {
		return a.agentGuardResolveTarget(raw)
	}
	return resolveAgentGuardTarget(raw)
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   a.cfg.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Requested-With", "X-Workspace-Org-Id"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", a.handleHealth)
	// 指标端点通常由 Prometheus/内网抓取，不走 workspace 鉴权。
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/api/public/workspaces", a.handlePublicWorkspaces)
	r.With(a.authMiddleware).Handle("/mcp", a.MCPStreamableHTTPHandler())
	r.With(a.authMiddleware).Handle("/mcp/sse", a.MCPHandler())

	r.Route("/api", func(r chi.Router) {
		r.Use(a.authMiddleware)

		r.Get("/me", a.handleMe)
		r.Get("/workspaces", a.handleWorkspaces)
		r.Post("/workspaces", a.handleCreateWorkspace)
		r.Get("/dashboard/summary", a.handleDashboardSummary)
		r.Get("/policies", a.handleListPolicies)
		r.Post("/policies", a.handleCreatePolicyRule)
		r.Post("/policies/simulate", a.handleSimulatePolicy)
		r.Get("/policies/{id}", a.handleGetPolicyRule)
		r.Put("/policies/{id}", a.handleUpdatePolicyRule)
		r.Delete("/policies/{id}", a.handleDeletePolicyRule)
		r.Get("/secrets", a.handleListSecrets)
		r.Post("/secrets", a.handleCreateSecret)
		r.Get("/secrets/{id}/usage", a.handleGetSecretUsage)
		r.Get("/secrets/{id}", a.handleGetSecret)
		r.Put("/secrets/{id}", a.handleUpdateSecret)
		r.Delete("/secrets/{id}", a.handleDeleteSecret)
		r.Get("/database/schema", a.handleDatabaseSchema)
		r.Get("/connectors", a.handleListConnectors)
		r.Post("/connectors", a.handleCreateConnector)
		r.Get("/connectors/{id}", a.handleGetConnector)
		r.Patch("/connectors/{id}", a.handlePatchConnector)
		r.Post("/connectors/{id}/sync", a.handleSyncConnector)

		r.Get("/tools", a.handleListTools)
		r.Post("/tools", a.handleCreateTool)
		r.Get("/tools/{id}", a.handleGetTool)
		r.Patch("/tools/{id}", a.handlePatchTool)

		r.Get("/approvals", a.handleListApprovals)
		r.Get("/approvals/stream", a.handleApprovalStream)
		r.Post("/approvals/{id}/approve", a.handleApproveApproval)
		r.Post("/approvals/{id}/reject", a.handleRejectApproval)

		r.Get("/tool-calls", a.handleListToolCalls)
		r.Get("/tool-calls/{id}", a.handleGetToolCall)
		r.Post("/tool-calls", a.handleCreateToolCall)
		r.Post("/agent-guard/evaluate", a.handleAgentGuardEvaluate)
	})
	a.mountEmbeddedFrontend(r)

	return r
}

func (a *App) mountEmbeddedFrontend(r chi.Router) {
	if !a.HasEmbeddedFrontend() {
		return
	}
	fileServer := http.FileServer(a.frontendFS)
	r.Handle("/assets/*", fileServer)
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if isReservedBackendPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		assetPath := strings.TrimPrefix(r.URL.Path, "/")
		if assetPath == "" {
			serveEmbeddedIndex(w, r, a.frontendFS)
			return
		}
		if file, err := a.frontendFS.Open(assetPath); err == nil {
			_ = file.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		serveEmbeddedIndex(w, r, a.frontendFS)
	})
}

func isReservedBackendPath(path string) bool {
	cleaned := "/" + strings.TrimPrefix(path, "/")
	return cleaned == "/health" ||
		cleaned == "/metrics" ||
		cleaned == "/api" ||
		cleaned == "/mcp" ||
		strings.HasPrefix(cleaned, "/api/") ||
		strings.HasPrefix(cleaned, "/mcp/")
}

func serveEmbeddedIndex(w http.ResponseWriter, r *http.Request, fsys http.FileSystem) {
	file, err := fsys.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	http.ServeContent(w, r, "index.html", time.Time{}, file)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	if err := a.store.Ping(r.Context()); err != nil {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    status,
		"store":     a.cfg.StoreDriver,
		"authMode":  a.cfg.AuthMode,
		"version":   "week-1-skeleton",
		"timestamp": time.Now().UTC(),
	})
}

func (a *App) handlePublicWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := a.store.ListWorkspaces(r.Context())
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": workspaces})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	writeJSON(w, http.StatusOK, reqCtx)
}

func (a *App) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	workspaces, err := a.store.ListWorkspaces(r.Context())
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": workspaces})
}

func (a *App) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireRole(reqCtx, roleOwner, roleAdmin); err != nil {
		a.respondError(w, err)
		return
	}

	var req createWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Slug) == "" || strings.TrimSpace(req.ZitadelOrganizationID) == "" {
		a.respondError(w, badRequest("name, slug and zitadelOrganizationId are required"))
		return
	}

	workspace, err := a.store.CreateWorkspace(r.Context(), model.CreateWorkspaceInput{
		Name:                  req.Name,
		Slug:                  req.Slug,
		ZitadelOrganizationID: req.ZitadelOrganizationID,
	})
	if err != nil {
		a.respondError(w, err)
		return
	}

	if err := a.ensureBuiltinTools(r.Context(), workspace.ID); err != nil {
		a.respondError(w, err)
		return
	}
	if err := a.ensureBuiltinConnectors(r.Context(), workspace.ID); err != nil {
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, workspace)
}

func (a *App) handleListTools(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	tools, err := a.store.ListTools(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tools})
}

func (a *App) handleCreateTool(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireManageTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req createToolRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	if strings.TrimSpace(req.Namespace) == "" || strings.TrimSpace(req.Name) == "" {
		a.respondError(w, badRequest("namespace and name are required"))
		return
	}

	tool, err := a.store.CreateTool(r.Context(), model.CreateToolInput{
		WorkspaceID:      reqCtx.Workspace.ID,
		Namespace:        req.Namespace,
		Name:             req.Name,
		DisplayName:      req.DisplayName,
		Description:      req.Description,
		OperationType:    req.OperationType,
		RiskLevel:        req.RiskLevel,
		RequiresApproval: req.RequiresApproval,
		InputSchemaJSON:  defaultJSON(req.InputSchemaJSON),
		OutputSchemaJSON: defaultJSON(req.OutputSchemaJSON),
		Enabled:          boolValue(req.Enabled, true),
	})
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tool)
}

func (a *App) handleGetTool(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	tool, err := a.store.GetToolByID(r.Context(), reqCtx.Workspace.ID, chi.URLParam(r, "id"))
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tool)
}

func (a *App) handleListToolCalls(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewAudit(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	query, err := parseToolCallListQuery(r)
	if err != nil {
		a.respondError(w, err)
		return
	}

	pagedCalls, err := a.store.ListToolCallsPage(r.Context(), reqCtx.Workspace.ID, query)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pagedCalls)
}

func (a *App) handleGetToolCall(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewAudit(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	call, err := a.store.GetToolCallByID(r.Context(), reqCtx.Workspace.ID, chi.URLParam(r, "id"))
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, call)
}

func (a *App) handleCreateToolCall(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireExecuteTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req createToolCallRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	if strings.TrimSpace(req.Tool) == "" {
		a.respondError(w, badRequest("tool is required"))
		return
	}

	response, err := a.createToolCall(r.Context(), reqCtx, req.Tool, req.Arguments)
	if err != nil {
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewApprovals(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	approvals, err := a.store.ListApprovalRequests(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": approvals})
}

func (a *App) handleApproveApproval(w http.ResponseWriter, r *http.Request) {
	a.handleApprovalDecision(w, r, "approved")
}

func (a *App) handleRejectApproval(w http.ResponseWriter, r *http.Request) {
	a.handleApprovalDecision(w, r, "rejected")
}

func (a *App) handleApprovalDecision(w http.ResponseWriter, r *http.Request, decision string) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireReviewApprovals(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	approvalID := chi.URLParam(r, "id")
	if strings.TrimSpace(approvalID) == "" {
		a.respondError(w, badRequest("approval id is required"))
		return
	}

	reviewReason, err := readApprovalReviewReason(r)
	if err != nil {
		a.respondError(w, err)
		return
	}

	approval, err := a.store.GetApprovalRequestByID(r.Context(), reqCtx.Workspace.ID, approvalID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	if strings.EqualFold(strings.TrimSpace(approval.Status), "expired") {
		a.respondError(w, store.ErrExpired)
		return
	}
	reviewer := approvalActorID(reqCtx)
	if approvalSelfReviewForbidden(reqCtx, approval.RequestedBy) {
		a.respondError(w, forbidden("approval requester cannot review their own approval"))
		return
	}

	call, err := a.store.GetToolCallByApprovalID(r.Context(), reqCtx.Workspace.ID, approvalID)
	if err != nil {
		a.respondError(w, err)
		return
	}

	updatedApproval, err := a.store.TransitionApprovalRequest(r.Context(), reqCtx.Workspace.ID, approvalID, "pending", model.UpdateApprovalRequestInput{
		Status:     decision,
		ReviewedBy: reviewer,
		Reason:     reviewReason,
	})
	if err != nil {
		a.respondError(w, err)
		return
	}

	if decision == "rejected" {
		updatedCall, err := a.store.UpdateToolCall(r.Context(), reqCtx.Workspace.ID, call.ID, model.UpdateToolCallInput{
			Status:             "rejected",
			DurationMs:         0,
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{}`),
			ErrorMessage:       "approval rejected",
			TraceID:            call.TraceID,
		})
		if err != nil {
			a.respondError(w, err)
			return
		}
		a.publishApprovalEvent(updatedApproval)
		writeJSON(w, http.StatusOK, approvalActionResponse{
			Approval: updatedApproval,
			ToolCall: updatedCall,
		})
		return
	}

	if strings.EqualFold(strings.TrimSpace(call.ToolKey), agentGuardEvaluateToolKey) {
		updatedCall, err := a.store.UpdateToolCall(r.Context(), reqCtx.Workspace.ID, call.ID, model.UpdateToolCallInput{
			Status:             "approval_required",
			DurationMs:         call.DurationMs,
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{}`),
			ErrorMessage:       "",
			TraceID:            call.TraceID,
		})
		if err != nil {
			a.respondError(w, err)
			return
		}
		a.publishApprovalEvent(updatedApproval)
		writeJSON(w, http.StatusOK, approvalActionResponse{
			Approval: updatedApproval,
			ToolCall: updatedCall,
		})
		return
	}

	tool, err := a.store.GetToolByKey(r.Context(), reqCtx.Workspace.ID, call.ToolKey)
	if err != nil {
		a.respondError(w, err)
		return
	}

	start := time.Now().UTC()
	// 审批通过后只能使用创建审批单时冻结的执行参数；禁止回退到公开脱敏 input，避免审批时被替换或用脱敏值执行真实写操作。
	executionInput := defaultJSON(call.InputExecutionJSON)
	decodedArgs, _ := decodeJSONValue(executionInput)
	connectorCtx, connectorSpan := telemetry.StartSpan(r.Context(), "agenttoolgate.connector.execute", attribute.String("tool.key", tool.Key()))
	resultPayload, resultJSON, execErr := a.executeTool(connectorCtx, tool, reqCtx.Workspace.ID, decodedArgs)
	if execErr != nil {
		telemetry.RecordError(connectorSpan, execErr)
	}
	connectorSpan.End()
	durationMs := time.Since(start).Milliseconds()
	status := "success"
	errorMessage := ""
	if execErr != nil {
		status = "failed"
		errorMessage = execErr.Error()
		resultJSON = json.RawMessage(`{}`)
	}

	updatedCall, err := a.store.UpdateToolCall(r.Context(), reqCtx.Workspace.ID, call.ID, model.UpdateToolCallInput{
		Status:             status,
		DurationMs:         durationMs,
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: redactToolOutputForAudit(tool.Key(), resultJSON),
		ErrorMessage:       errorMessage,
		TraceID:            call.TraceID,
	})
	if err != nil {
		a.respondError(w, err)
		return
	}
	a.publishApprovalEvent(updatedApproval)

	writeJSON(w, http.StatusOK, approvalActionResponse{
		Approval: updatedApproval,
		ToolCall: updatedCall,
		Result:   resultPayload,
	})
}

func (a *App) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewDashboard(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	calls, err := a.store.ListToolCalls(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	summary := summarizeCalls(reqCtx.Workspace.ID, calls)
	writeJSON(w, http.StatusOK, summary)
}

func (a *App) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireViewPolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}
	rules, err := a.store.ListPolicyRules(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rules})
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workspaceOrgID := a.requestedWorkspaceOrgID(r)
		identity, err := a.authenticator.Authenticate(
			r.Context(),
			bearerToken(r.Header.Get("Authorization")),
			workspaceOrgID,
		)
		if err != nil {
			a.respondError(w, err)
			return
		}

		workspace, user, err := a.authenticator.ResolvePrincipal(r.Context(), a.store, identity)
		if err != nil {
			a.respondError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), requestContextKey{}, RequestContext{
			Identity:  identity,
			Workspace: workspace,
			User:      user,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) requestedWorkspaceOrgID(r *http.Request) string {
	workspaceOrgID := strings.TrimSpace(r.Header.Get("X-Workspace-Org-Id"))
	if workspaceOrgID != "" {
		return workspaceOrgID
	}

	// 只在本地模式下放开 Approval SSE 的 query fallback，因为 EventSource 不能稳定携带自定义 header。
	if a.cfg.UsesOIDC() {
		return ""
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/approvals/stream" {
		return strings.TrimSpace(r.URL.Query().Get("workspaceOrgId"))
	}
	return ""
}

func (a *App) ensureBuiltinTools(ctx context.Context, workspaceID string) error {
	for _, input := range model.BuiltinToolInputs(workspaceID) {
		if _, err := a.store.GetToolByKey(ctx, workspaceID, input.Namespace+"."+input.Name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := a.store.CreateTool(ctx, input); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (a *App) respondError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, store.ErrExpired):
		status = http.StatusConflict
	case errors.Is(err, errRateLimited):
		status = http.StatusTooManyRequests
	case errors.Is(err, errBadRequest):
		status = http.StatusBadRequest
	case errors.Is(err, errForbidden):
		status = http.StatusForbidden
	case strings.Contains(strings.ToLower(err.Error()), "missing bearer token"):
		status = http.StatusUnauthorized
	case strings.Contains(strings.ToLower(err.Error()), "verify id token"):
		status = http.StatusUnauthorized
	case strings.Contains(strings.ToLower(err.Error()), "missing request context"):
		status = http.StatusUnauthorized
	case strings.Contains(strings.ToLower(err.Error()), "disabled"):
		status = http.StatusConflict
	case strings.Contains(strings.ToLower(err.Error()), "not supported"):
		status = http.StatusNotImplemented
	}

	a.logger.Warn("request failed", "status", status, "error", err)
	message := err.Error()
	if errors.Is(err, errForbidden) {
		message = "forbidden"
	}
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}

func requestContextFrom(ctx context.Context) (RequestContext, bool) {
	value := ctx.Value(requestContextKey{})
	if value == nil {
		return RequestContext{}, false
	}
	reqCtx, ok := value.(RequestContext)
	return reqCtx, ok
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func readJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return nil
}

func readApprovalReviewReason(r *http.Request) (string, error) {
	var req approvalReviewRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", fmt.Errorf("%w: %v", errBadRequest, err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err == nil {
		return "", badRequest("approval review request must contain a single JSON object")
	} else if !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: %v", errBadRequest, err)
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) > 500 {
		return "", badRequest("approval reason is too long")
	}
	for _, ch := range reason {
		if unicode.IsControl(ch) && ch != '\n' && ch != '\r' && ch != '\t' {
			return "", badRequest("approval reason contains control characters")
		}
	}
	return reason, nil
}

func parseToolCallListQuery(r *http.Request) (model.ToolCallQuery, error) {
	query := model.ToolCallQuery{
		Page:     1,
		PageSize: 50,
	}

	rawTool := strings.TrimSpace(r.URL.Query().Get("tool"))
	if rawTool != "" {
		query.Tool = rawTool
	}

	statuses := make([]string, 0)
	for _, raw := range r.URL.Query()["status"] {
		for _, status := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(status)
			if trimmed != "" {
				statuses = append(statuses, trimmed)
			}
		}
	}
	if len(statuses) > 0 {
		query.Statuses = statuses
	}

	if rawFrom := strings.TrimSpace(r.URL.Query().Get("from")); rawFrom != "" {
		parsed, err := parseToolCallTimeQueryParam(rawFrom, false)
		if err != nil {
			return model.ToolCallQuery{}, badRequest("from must be a valid ISO 8601 timestamp or date")
		}
		query.From = &parsed
	}
	if rawTo := strings.TrimSpace(r.URL.Query().Get("to")); rawTo != "" {
		parsed, err := parseToolCallTimeQueryParam(rawTo, true)
		if err != nil {
			return model.ToolCallQuery{}, badRequest("to must be a valid ISO 8601 timestamp or date")
		}
		query.To = &parsed
	}

	if rawPage := strings.TrimSpace(r.URL.Query().Get("page")); rawPage != "" {
		page, err := strconv.Atoi(rawPage)
		if err != nil || page <= 0 {
			return model.ToolCallQuery{}, badRequest("page must be a positive integer")
		}
		query.Page = page
	}
	if rawPageSize := strings.TrimSpace(r.URL.Query().Get("pageSize")); rawPageSize != "" {
		pageSize, err := strconv.Atoi(rawPageSize)
		if err != nil || pageSize <= 0 {
			return model.ToolCallQuery{}, badRequest("pageSize must be a positive integer")
		}
		if pageSize > 200 {
			pageSize = 200
		}
		query.PageSize = pageSize
	}

	return query, nil
}

func parseToolCallTimeQueryParam(raw string, endOfDay bool) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		parsed = parsed.UTC()
		if endOfDay {
			return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC), nil
		}
		return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("invalid time")
}

func badRequest(message string) error {
	return fmt.Errorf("%w: %s", errBadRequest, message)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func redactJSONByKey(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return defaultJSON(value)
	}
	redacted := redactJSONValueByKey(decoded)
	raw, err := json.Marshal(redacted)
	if err != nil {
		return defaultJSON(value)
	}
	return raw
}

func redactToolInputForAudit(toolKey string, value json.RawMessage) json.RawMessage {
	if strings.EqualFold(strings.TrimSpace(toolKey), "database.query") {
		return redactDatabaseQueryInputForAudit(value)
	}
	if namespace, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(toolKey)), "."); ok && strings.HasPrefix(namespace, "mcp_") {
		return redactMCPToolInputForAudit(value)
	}
	return redactJSONByKey(value)
}

func redactJSONValueByKey(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSecretReferenceJSONKey(key) {
				redacted[key] = item
				continue
			}
			if isSensitiveJSONKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = redactJSONValueByKey(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactJSONValueByKey(item)
		}
		return redacted
	default:
		return value
	}
}

func isSensitiveJSONKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	compact := strings.NewReplacer("-", "", "_", "", " ", "").Replace(normalized)
	sensitiveTokens := []string{
		"password",
		"passwd",
		"secret",
		"token",
		"session",
		"api_key",
		"access_key",
		"private_key",
		"authorization",
		"cookie",
		"host",
	}
	for _, token := range sensitiveTokens {
		compactToken := strings.NewReplacer("-", "", "_", "", " ", "").Replace(token)
		if normalized == token || strings.Contains(normalized, token) || compact == compactToken || strings.Contains(compact, compactToken) {
			return true
		}
	}
	return false
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func summarizeCalls(workspaceID string, calls []model.ToolCall) model.DashboardSummary {
	summary := model.DashboardSummary{WorkspaceID: workspaceID}
	if len(calls) == 0 {
		return summary
	}

	toolCounts := map[string]int64{}
	errorCounts := map[string]int64{}
	var totalDuration int64
	for _, call := range calls {
		summary.TotalCalls++
		totalDuration += call.DurationMs
		toolCounts[call.ToolKey]++
		switch strings.ToLower(call.Status) {
		case "success":
			summary.SuccessCalls++
		case "failed":
			summary.FailedCalls++
		case "approval_required":
			summary.PendingApprovalCalls++
		default:
			summary.FailedCalls++
		}
		if strings.TrimSpace(call.ErrorMessage) != "" {
			errorCounts[call.ErrorMessage]++
		}
	}

	summary.AverageDurationMs = math.Round((float64(totalDuration)/float64(summary.TotalCalls))*10) / 10
	summary.TopTools = topToolCounts(toolCounts, 3)
	summary.TopErrors = topErrorCounts(errorCounts, 3)
	return summary
}

func topToolCounts(counts map[string]int64, limit int) []model.DashboardItem {
	type pair struct {
		key   string
		count int64
	}
	items := make([]pair, 0, len(counts))
	for key, count := range counts {
		items = append(items, pair{key: key, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})
	result := make([]model.DashboardItem, 0, limit)
	for i := 0; i < len(items) && i < limit; i++ {
		result = append(result, model.DashboardItem{ToolKey: items[i].key, Count: items[i].count})
	}
	return result
}

func topErrorCounts(counts map[string]int64, limit int) []model.DashboardError {
	type pair struct {
		key   string
		count int64
	}
	items := make([]pair, 0, len(counts))
	for key, count := range counts {
		items = append(items, pair{key: key, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})
	result := make([]model.DashboardError, 0, limit)
	for i := 0; i < len(items) && i < limit; i++ {
		result = append(result, model.DashboardError{Message: items[i].key, Count: items[i].count})
	}
	return result
}

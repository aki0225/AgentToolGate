package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/policy"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

const agentGuardEvaluateToolKey = "agent_guard.evaluate"
const agentGuardApprovalTTL = 10 * time.Minute

type agentGuardEvaluateRequest struct {
	Adapter              string `json:"adapter"`
	Tool                 string `json:"tool"`
	ActionType           string `json:"actionType"`
	Target               string `json:"target"`
	IsScript             bool   `json:"isScript"`
	ContentEncoding      string `json:"contentEncoding"`
	Content              string `json:"content"`
	TicketID             string `json:"ticketId,omitempty"`
	ResolvedFileIdentity string `json:"resolvedFileIdentity,omitempty"`
	ParentIdentity       string `json:"parentIdentity,omitempty"`
}

type agentGuardDecisionResponse struct {
	Decision       string                 `json:"decision"`
	Reason         string                 `json:"reason,omitempty"`
	ApprovalID     string                 `json:"approvalId,omitempty"`
	ApprovalStatus string                 `json:"approvalStatus,omitempty"`
	CallID         string                 `json:"callId,omitempty"`
	Fingerprint    string                 `json:"fingerprint,omitempty"`
	Explanation    *agentGuardExplanation `json:"explanation,omitempty"`
}

type agentGuardExplanation struct {
	TargetCategory string   `json:"targetCategory"`
	RiskLevel      string   `json:"riskLevel"`
	MatchedRule    string   `json:"matchedRule"`
	Signals        []string `json:"signals"`
}

func (a *App) handleAgentGuardEvaluate(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireExecuteTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req agentGuardEvaluateRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	if strings.TrimSpace(req.Tool) == "" {
		a.respondError(w, badRequest("tool is required"))
		return
	}
	if strings.TrimSpace(req.ActionType) == "" {
		a.respondError(w, badRequest("actionType is required"))
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		a.respondError(w, badRequest("target is required"))
		return
	}

	response, err := a.evaluateAgentGuard(r.Context(), reqCtx, req)
	if err != nil {
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (a *App) evaluateAgentGuard(ctx context.Context, reqCtx RequestContext, req agentGuardEvaluateRequest) (agentGuardDecisionResponse, error) {
	ctx, requestSpan := telemetry.StartSpan(ctx, "agenttoolgate.request",
		attribute.String("tool.key", agentGuardEvaluateToolKey),
		attribute.String("agent_guard.adapter", strings.TrimSpace(req.Adapter)),
	)
	defer requestSpan.End()

	_, authSpan := telemetry.StartSpan(ctx, "agenttoolgate.auth",
		attribute.String("workspace.id", reqCtx.Workspace.ID),
		attribute.String("user.role", reqCtx.User.Role),
	)
	authSpan.End()

	tool, err := a.store.GetToolByKey(ctx, reqCtx.Workspace.ID, agentGuardEvaluateToolKey)
	if err != nil {
		return agentGuardDecisionResponse{}, err
	}

	normalizedTarget := normalizeAgentGuardTarget(req.Target)
	decodedContent, err := decodeAgentGuardContent(req.ContentEncoding, req.Content)
	if err != nil {
		return agentGuardDecisionResponse{}, err
	}
	targetResolution := a.resolveAgentGuardTarget(normalizedTarget)
	targetCategory := classifyAgentGuardTargetCategoryWithResolution(normalizedTarget, targetResolution)
	contentSensitive := containsSensitiveAgentGuardContent(decodedContent)
	contentHash := hashAgentGuardPayload(decodedContent)
	scriptHash := ""
	if req.IsScript {
		scriptHash = contentHash
	}
	riskLevel := deriveAgentGuardRisk(req.ActionType, normalizedTarget, req.IsScript, decodedContent, targetResolution.ResolvedPath, targetResolution.ResolvedParentPath, targetResolution.ParentIdentity)
	operationType := deriveAgentGuardOperationType(req.ActionType, riskLevel)
	traceID := telemetry.TraceID(ctx)
	if traceID == "" {
		traceID = uuid.NewString()
	}
	requestID := "req_" + uuid.NewString()
	fingerprint := hashAgentGuardPayload(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Adapter)),
		reqCtx.Workspace.ID,
		reqCtx.User.ID,
		reqCtx.Identity.Subject,
		strings.ToLower(strings.TrimSpace(req.Tool)),
		strings.ToLower(strings.TrimSpace(req.ActionType)),
		targetResolution.CanonicalTarget,
		targetResolution.ResolvedFileIdentity,
		targetResolution.ParentIdentity,
		contentHash,
		scriptHash,
	}, "\x00"))

	decisionPayload := map[string]any{
		"adapter":              strings.TrimSpace(req.Adapter),
		"tool":                 strings.TrimSpace(req.Tool),
		"actionType":           strings.ToLower(strings.TrimSpace(req.ActionType)),
		"target":               strings.TrimSpace(req.Target),
		"isScript":             req.IsScript,
		"contentEncoding":      strings.ToLower(strings.TrimSpace(req.ContentEncoding)),
		"content":              decodedContent,
		"contentHash":          contentHash,
		"scriptHash":           scriptHash,
		"targetCategory":       targetCategory,
		"contentSensitive":     contentSensitive,
		"canonicalTarget":      targetResolution.CanonicalTarget,
		"resolvedPath":         targetResolution.ResolvedPath,
		"resolvedParentPath":   targetResolution.ResolvedParentPath,
		"resolvedFileIdentity": targetResolution.ResolvedFileIdentity,
		"parentIdentity":       targetResolution.ParentIdentity,
		"riskLevel":            riskLevel,
		"fingerprint":          fingerprint,
	}
	decisionPayloadJSON, err := json.Marshal(decisionPayload)
	if err != nil {
		return agentGuardDecisionResponse{}, err
	}
	inputRedactedJSON := redactAgentGuardInputForAudit(decisionPayloadJSON)

	_, approvalSpan := telemetry.StartSpan(ctx, "agenttoolgate.policy.evaluate",
		attribute.String("tool.namespace", tool.Namespace),
		attribute.String("tool.name", tool.Name),
		attribute.String("agent_guard.operation_type", operationType),
		attribute.String("agent_guard.risk_level", riskLevel),
	)
	policyDecision, policyReason, policyRuleName := a.decideAgentGuardPolicy(reqCtx, operationType, riskLevel, req.ActionType, targetCategory, contentSensitive)
	approvalSpan.SetAttributes(attribute.String("policy.decision", policyDecision))
	approvalSpan.End()
	telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "triggered")
	explanation := buildAgentGuardExplanation(targetCategory, riskLevel, policyRuleName, normalizedTarget, targetResolution, req.ContentEncoding, req.Content, decodedContent)

	if isAgentGuardStrictTargetCategory(targetCategory) && targetResolution.TargetExists && !targetResolution.TargetIdentityStable {
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "sensitive path without stable file identity", fingerprint, riskLevel, "", explanation)
	}

	existingApproval, existingCall, found, err := a.lookupAgentGuardApproval(ctx, reqCtx.Workspace.ID, fingerprint)
	if err != nil {
		return agentGuardDecisionResponse{}, err
	}
	now := time.Now().UTC()

	if strings.TrimSpace(req.TicketID) != "" {
		return a.evaluateAgentGuardWithTicket(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, policyDecision, policyReason, policyRuleName, traceID, requestID, existingApproval, existingCall, found, fingerprint, riskLevel, explanation)
	}

	if found && approvalIsExpiredAt(now, existingApproval) {
		if err := a.expireAgentGuardApprovalMemory(ctx, reqCtx.Workspace.ID, existingApproval); err != nil {
			return agentGuardDecisionResponse{}, err
		}
		found = false
	}

	if found {
		switch strings.ToLower(strings.TrimSpace(existingApproval.Status)) {
		case "pending":
			return agentGuardDecisionResponse{
				Decision:       "deny_with_ticket",
				Reason:         approvalReasonForAgentGuard(policyReason, existingApproval),
				ApprovalID:     existingApproval.ID,
				ApprovalStatus: existingApproval.Status,
				CallID:         existingCall.ID,
				Fingerprint:    fingerprint,
				Explanation:    explanation,
			}, nil
		case "approved", "consumed":
			if canReuseAgentGuardApprovedFingerprint(riskLevel) {
				return a.recordAgentGuardRememberedAllow(ctx, reqCtx, tool, inputRedactedJSON, traceID, requestID, policyReason, policyRuleName, existingApproval, fingerprint, riskLevel, explanation)
			}
			if strings.EqualFold(strings.TrimSpace(existingApproval.Status), "approved") {
				telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "approval_required")
				return agentGuardDecisionResponse{
					Decision:       "deny_with_ticket",
					Reason:         approvalReasonForAgentGuard(policyReason, existingApproval),
					ApprovalID:     existingApproval.ID,
					ApprovalStatus: existingApproval.Status,
					CallID:         existingCall.ID,
					Fingerprint:    fingerprint,
					Explanation:    explanation,
				}, nil
			}
			telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "approval_required")
			return a.recordAgentGuardApproval(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, policyReason, fingerprint, riskLevel, explanation)
		default:
			return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, policyReason, fingerprint, riskLevel, "", explanation)
		}
	}

	switch policyDecision {
	case policyAllow:
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "allow")
		return a.recordAgentGuardSuccess(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, policyReason, fingerprint, riskLevel, explanation)
	case policyDeny:
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, policyReason, fingerprint, riskLevel, "", explanation)
	case policyRequireApproval:
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "approval_required")
		return a.recordAgentGuardApproval(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, policyReason, fingerprint, riskLevel, explanation)
	default:
		return agentGuardDecisionResponse{}, fmt.Errorf("unsupported agent guard decision %q", policyDecision)
	}
}

func (a *App) evaluateAgentGuardWithTicket(ctx context.Context, reqCtx RequestContext, tool model.Tool, req agentGuardEvaluateRequest, decisionPayloadJSON, inputRedactedJSON json.RawMessage, policyDecision, policyReason, policyRuleName, traceID, requestID string, existingApproval model.ApprovalRequest, existingCall model.ToolCall, found bool, fingerprint, riskLevel string, explanation *agentGuardExplanation) (agentGuardDecisionResponse, error) {
	approval := existingApproval
	if ticketID := strings.TrimSpace(req.TicketID); ticketID != "" {
		fetchedApproval, err := a.store.GetApprovalRequestByID(ctx, reqCtx.Workspace.ID, ticketID)
		if err != nil {
			return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "invalid ticket", fingerprint, riskLevel, "", explanation)
		}
		approval = fetchedApproval
	}
	if strings.TrimSpace(approval.ID) == "" {
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "invalid ticket", fingerprint, riskLevel, "", explanation)
	}
	if !strings.EqualFold(strings.TrimSpace(approval.WorkspaceID), reqCtx.Workspace.ID) {
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "invalid ticket", fingerprint, riskLevel, "", explanation)
	}
	if approvalIsExpiredAt(time.Now().UTC(), approval) {
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "ticket expired", fingerprint, riskLevel, "", explanation)
	}
	if strings.TrimSpace(approval.Fingerprint) == "" || !strings.EqualFold(strings.TrimSpace(approval.Fingerprint), strings.TrimSpace(fingerprint)) {
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "ticket fingerprint mismatch", fingerprint, riskLevel, "", explanation)
	}

	switch strings.ToLower(strings.TrimSpace(approval.Status)) {
	case "approved":
		consumedApproval, err := a.store.TransitionApprovalRequest(ctx, reqCtx.Workspace.ID, approval.ID, "approved", model.UpdateApprovalRequestInput{
			Status:     "consumed",
			ReviewedBy: approval.ReviewedBy,
			Reason:     approval.Reason,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrExpired) {
				telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
				return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "ticket already consumed", fingerprint, riskLevel, "", explanation)
			}
			return agentGuardDecisionResponse{}, err
		}
		call, err := a.store.GetToolCallByApprovalID(ctx, reqCtx.Workspace.ID, approval.ID)
		if err != nil {
			return agentGuardDecisionResponse{}, err
		}
		updatedCall, err := a.store.UpdateToolCall(ctx, reqCtx.Workspace.ID, call.ID, model.UpdateToolCallInput{
			Status:             "success",
			DurationMs:         0,
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{}`),
			ErrorMessage:       "",
			TraceID:            call.TraceID,
		})
		if err != nil {
			return agentGuardDecisionResponse{}, err
		}
		a.publishApprovalEvent(consumedApproval)
		telemetry.RecordToolCall(tool.Key(), "success", 0)
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "retry_allowed")
		return agentGuardDecisionResponse{
			Decision:       "allow",
			Reason:         policyReason,
			ApprovalID:     approval.ID,
			ApprovalStatus: consumedApproval.Status,
			CallID:         updatedCall.ID,
			Fingerprint:    fingerprint,
			Explanation:    explanation,
		}, nil
	case "pending":
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "approval_required")
		return agentGuardDecisionResponse{
			Decision:       "deny_with_ticket",
			Reason:         approvalReasonForAgentGuard(policyReason, approval),
			ApprovalID:     approval.ID,
			ApprovalStatus: approval.Status,
			CallID:         existingCall.ID,
			Fingerprint:    fingerprint,
			Explanation:    explanation,
		}, nil
	case "consumed", "rejected", "expired":
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "ticket already consumed", fingerprint, riskLevel, "", explanation)
	default:
		telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "denied")
		return a.recordAgentGuardDenied(ctx, reqCtx, tool, req, decisionPayloadJSON, inputRedactedJSON, traceID, requestID, "ticket is not approved yet", fingerprint, riskLevel, "", explanation)
	}
}

func (a *App) recordAgentGuardRememberedAllow(ctx context.Context, reqCtx RequestContext, tool model.Tool, inputRedactedJSON json.RawMessage, traceID, requestID, policyReason, policyRuleName string, existingApproval model.ApprovalRequest, fingerprint, riskLevel string, explanation *agentGuardExplanation) (agentGuardDecisionResponse, error) {
	_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "success"))
	call, err := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        reqCtx.Workspace.ID,
		RequestID:          requestID,
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.User.Name,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "success",
		RiskLevel:          riskLevel,
		PolicyDecision:     policyAllow,
		ApprovalID:         existingApproval.ID,
		DurationMs:         0,
		InputRedactedJSON:  inputRedactedJSON,
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		Explanation:        modelToolCallExplanation(explanation),
		ErrorMessage:       "",
		TraceID:            traceID,
	})
	if err != nil {
		telemetry.RecordError(auditSpan, err)
		auditSpan.End()
		return agentGuardDecisionResponse{}, err
	}
	auditSpan.End()
	telemetry.RecordToolCall(tool.Key(), "success", 0)
	telemetry.RecordAgentGuardRuleOutcome(policyRuleName, "allow")
	return agentGuardDecisionResponse{
		Decision:       "allow",
		Reason:         policyReason,
		ApprovalID:     existingApproval.ID,
		ApprovalStatus: existingApproval.Status,
		CallID:         call.ID,
		Fingerprint:    fingerprint,
		Explanation:    explanation,
	}, nil
}

func (a *App) expireAgentGuardApprovalMemory(ctx context.Context, workspaceID string, approval model.ApprovalRequest) error {
	switch strings.ToLower(strings.TrimSpace(approval.Status)) {
	case "pending", "approved":
		_, err := a.store.TransitionApprovalRequest(ctx, workspaceID, approval.ID, approval.Status, model.UpdateApprovalRequestInput{
			Status:     "expired",
			ReviewedBy: approval.ReviewedBy,
			Reason:     approval.Reason,
		})
		if err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrExpired) && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (a *App) recordAgentGuardApproval(ctx context.Context, reqCtx RequestContext, tool model.Tool, req agentGuardEvaluateRequest, decisionPayloadJSON, inputRedactedJSON json.RawMessage, traceID, requestID, policyReason, fingerprint, riskLevel string, explanation *agentGuardExplanation) (agentGuardDecisionResponse, error) {
	approvalSpanCtx, approvalSpan := telemetry.StartSpan(ctx, "agenttoolgate.approval.check", attribute.String("policy.decision", policyRequireApproval))
	targetResolution := a.resolveAgentGuardTarget(normalizeAgentGuardTarget(req.Target))
	approval, err := a.store.CreateApprovalRequest(approvalSpanCtx, model.CreateApprovalRequestInput{
		WorkspaceID:     reqCtx.Workspace.ID,
		ToolKey:         tool.Key(),
		ToolDisplayName: tool.DisplayName,
		RequestedBy:     approvalRequestedBy(reqCtx.User.Name, reqCtx.Identity.Subject),
		Reason:          approvalReasonForAgentGuard(policyReason, model.ApprovalRequest{Target: req.Target, CanonicalTarget: targetResolution.CanonicalTarget}),
		Fingerprint:     fingerprint,
		Adapter:         strings.TrimSpace(req.Adapter),
		ActionType:      strings.TrimSpace(req.ActionType),
		Target:          strings.TrimSpace(req.Target),
		CanonicalTarget: targetResolution.CanonicalTarget,
		ContentEncoding: strings.TrimSpace(req.ContentEncoding),
		ContentHash:     hashAgentGuardPayload(decodedAgentGuardContentOrEmpty(req.ContentEncoding, req.Content)),
		ScriptHash: func() string {
			if req.IsScript {
				return hashAgentGuardPayload(decodedAgentGuardContentOrEmpty(req.ContentEncoding, req.Content))
			}
			return ""
		}(),
		ResolvedFileIdentity: targetResolution.ResolvedFileIdentity,
		ParentIdentity:       targetResolution.ParentIdentity,
		DecisionPayloadJSON:  decisionPayloadJSON,
		TTL:                  agentGuardApprovalTTL,
	})
	if err != nil {
		telemetry.RecordError(approvalSpan, err)
		approvalSpan.End()
		return agentGuardDecisionResponse{}, err
	}
	approvalSpan.End()

	_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "approval_required"))
	call, callErr := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        reqCtx.Workspace.ID,
		RequestID:          requestID,
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.Identity.Name,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          riskLevel,
		PolicyDecision:     policyRequireApproval,
		ApprovalID:         approval.ID,
		DurationMs:         0,
		InputRedactedJSON:  inputRedactedJSON,
		InputExecutionJSON: decisionPayloadJSON,
		OutputRedactedJSON: json.RawMessage(`{}`),
		Explanation:        modelToolCallExplanation(explanation),
		ErrorMessage:       "",
		TraceID:            traceID,
	})
	if callErr != nil {
		telemetry.RecordError(auditSpan, callErr)
		auditSpan.End()
		return agentGuardDecisionResponse{}, callErr
	}
	auditSpan.End()

	a.publishApprovalEvent(approval)
	telemetry.RecordToolCall(tool.Key(), "approval_required", 0)
	return agentGuardDecisionResponse{
		Decision:       "deny_with_ticket",
		Reason:         approvalReasonForAgentGuard(policyReason, approval),
		ApprovalID:     approval.ID,
		ApprovalStatus: approval.Status,
		CallID:         call.ID,
		Fingerprint:    fingerprint,
		Explanation:    explanation,
	}, nil
}

func (a *App) recordAgentGuardSuccess(ctx context.Context, reqCtx RequestContext, tool model.Tool, req agentGuardEvaluateRequest, decisionPayloadJSON, inputRedactedJSON json.RawMessage, traceID, requestID, policyReason, fingerprint, riskLevel string, explanation *agentGuardExplanation) (agentGuardDecisionResponse, error) {
	_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "success"))
	call, err := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        reqCtx.Workspace.ID,
		RequestID:          requestID,
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.User.Name,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "success",
		RiskLevel:          riskLevel,
		PolicyDecision:     policyAllow,
		ApprovalID:         "",
		DurationMs:         0,
		InputRedactedJSON:  inputRedactedJSON,
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		Explanation:        modelToolCallExplanation(explanation),
		ErrorMessage:       "",
		TraceID:            traceID,
	})
	if err != nil {
		telemetry.RecordError(auditSpan, err)
		auditSpan.End()
		return agentGuardDecisionResponse{}, err
	}
	auditSpan.End()
	telemetry.RecordToolCall(tool.Key(), "success", 0)
	return agentGuardDecisionResponse{
		Decision:    "allow",
		Reason:      policyReason,
		CallID:      call.ID,
		Fingerprint: fingerprint,
		Explanation: explanation,
	}, nil
}

func (a *App) recordAgentGuardDenied(ctx context.Context, reqCtx RequestContext, tool model.Tool, req agentGuardEvaluateRequest, decisionPayloadJSON, inputRedactedJSON json.RawMessage, traceID, requestID, reason, fingerprint, riskLevel, approvalID string, explanation *agentGuardExplanation) (agentGuardDecisionResponse, error) {
	_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "denied"))
	call, err := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        reqCtx.Workspace.ID,
		RequestID:          requestID,
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.User.Name,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "denied",
		RiskLevel:          riskLevel,
		PolicyDecision:     policyDeny,
		ApprovalID:         strings.TrimSpace(approvalID),
		DurationMs:         0,
		InputRedactedJSON:  inputRedactedJSON,
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		Explanation:        modelToolCallExplanation(explanation),
		ErrorMessage:       reason,
		TraceID:            traceID,
	})
	if err != nil {
		telemetry.RecordError(auditSpan, err)
		auditSpan.End()
		return agentGuardDecisionResponse{}, err
	}
	auditSpan.End()
	telemetry.RecordToolCall(tool.Key(), "denied", 0)
	return agentGuardDecisionResponse{
		Decision:    "deny",
		Reason:      reason,
		CallID:      call.ID,
		Fingerprint: fingerprint,
		Explanation: explanation,
	}, nil
}

func (a *App) lookupAgentGuardApproval(ctx context.Context, workspaceID, fingerprint string) (model.ApprovalRequest, model.ToolCall, bool, error) {
	approvals, err := a.store.ListApprovalRequests(ctx, workspaceID)
	if err != nil {
		return model.ApprovalRequest{}, model.ToolCall{}, false, err
	}
	for _, approval := range approvals {
		if !strings.EqualFold(strings.TrimSpace(approval.Fingerprint), strings.TrimSpace(fingerprint)) {
			continue
		}
		call, err := a.store.GetToolCallByApprovalID(ctx, workspaceID, approval.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return approval, model.ToolCall{}, true, nil
			}
			return model.ApprovalRequest{}, model.ToolCall{}, false, err
		}
		return approval, call, true, nil
	}
	return model.ApprovalRequest{}, model.ToolCall{}, false, nil
}

func (a *App) decideAgentGuardPolicy(reqCtx RequestContext, operationType, riskLevel, actionType, targetCategory string, contentSensitive bool) (string, string, string) {
	if a.policies == nil {
		a.policies = policy.NewDefaultEngine()
	}
	decision := a.policies.Evaluate(policy.Input{
		ToolNamespace:    "agent_guard",
		ToolName:         "evaluate",
		OperationType:    operationType,
		UserRole:         reqCtx.User.Role,
		RiskLevel:        riskLevel,
		ActionType:       strings.ToLower(strings.TrimSpace(actionType)),
		TargetCategory:   strings.ToLower(strings.TrimSpace(targetCategory)),
		ContentSensitive: contentSensitive,
		RequiresApproval: riskLevelRank(riskLevel) >= riskLevelRank("high") || strings.EqualFold(strings.TrimSpace(targetCategory), "sensitive") || contentSensitive,
		ToolEnabled:      true,
		SupportedTool:    true,
	})
	return string(decision.Effect), agentGuardReason(decision.Reason, riskLevel, operationType), decision.RuleName
}

func agentGuardReason(policyReason, riskLevel, operationType string) string {
	if strings.TrimSpace(policyReason) != "" {
		return policyReason
	}
	if strings.EqualFold(strings.TrimSpace(operationType), "write") {
		return "high risk local action requires approval"
	}
	if riskLevelRank(riskLevel) >= riskLevelRank("high") {
		return "high risk local action requires approval"
	}
	return "local action is allowed"
}

func approvalReasonForAgentGuard(policyReason string, approval model.ApprovalRequest) string {
	if strings.TrimSpace(policyReason) != "" {
		return policyReason
	}
	if strings.TrimSpace(approval.Reason) != "" {
		return approval.Reason
	}
	if strings.TrimSpace(approval.Target) != "" {
		return fmt.Sprintf("approval required for %s", approval.Target)
	}
	return "approval required"
}

func canReuseAgentGuardApprovedFingerprint(riskLevel string) bool {
	return riskLevelRank(riskLevel) < riskLevelRank("high")
}

func isAgentGuardStrictTargetCategory(targetCategory string) bool {
	switch strings.ToLower(strings.TrimSpace(targetCategory)) {
	case "sensitive", "self_tamper":
		return true
	default:
		return false
	}
}

func approvalIsExpiredAt(now time.Time, approval model.ApprovalRequest) bool {
	if approval.ExpiresAt.IsZero() {
		return false
	}
	return !approval.ExpiresAt.After(now.UTC())
}

func deriveAgentGuardOperationType(actionType, riskLevel string) string {
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case "read", "inspect", "view", "list", "get":
		return "read"
	case "write", "create", "update", "delete", "patch", "post":
		return "write"
	case "exec", "execute", "run":
		return "exec"
	default:
		return "write"
	}
}

func deriveAgentGuardRisk(actionType, target string, isScript bool, content string, resolvedTargets ...string) string {
	risk := "low"
	lowerContent := strings.ToLower(content)

	targets := append([]string{target}, resolvedTargets...)
	for _, candidate := range targets {
		normalizedTarget := normalizeAgentGuardTarget(candidate)
		if isAgentGuardSensitiveTarget(normalizedTarget) || isAgentGuardSelfTamperTarget(normalizedTarget) {
			risk = maxRiskLevel(risk, "high")
			break
		}
	}
	if containsSensitiveAgentGuardContent(lowerContent) {
		risk = maxRiskLevel(risk, "high")
	}
	if isScript {
		if containsHiddenScriptExecutionFeatures(lowerContent) {
			risk = maxRiskLevel(risk, "high")
		} else if containsHiddenScriptExecutionFeaturesInDecodedBase64(content) {
			risk = maxRiskLevel(risk, "high")
		} else if containsBase64EncodedPayload(lowerContent) {
			risk = maxRiskLevel(risk, "medium")
		}
	}
	switch strings.ToLower(strings.TrimSpace(actionType)) {
	case "write", "create", "update", "delete", "patch", "post", "exec", "execute", "run":
		risk = maxRiskLevel(risk, "medium")
	}
	return risk
}

func isAgentGuardSensitiveTarget(target string) bool {
	if target == "" {
		return false
	}
	if agentGuardPathMatchesExactFile(target, `.env`) {
		return true
	}
	sensitiveDirs := []string{
		`.ssh`,
		`.git/hooks`,
		`appdata/roaming/microsoft/windows/start menu/programs/startup`,
		`windows/system32/config`,
	}
	for _, dir := range sensitiveDirs {
		if agentGuardPathMatchesDirOrDescendant(target, dir) {
			return true
		}
	}
	sensitiveSegments := []string{
		`startup`,
		`credentials`,
		`secrets`,
	}
	for _, segment := range sensitiveSegments {
		if agentGuardPathHasSegment(target, segment) {
			return true
		}
	}
	return false
}

func containsSensitiveAgentGuardContent(content string) bool {
	if content == "" {
		return false
	}
	lower := strings.ToLower(content)
	keywords := []string{"password", "secret", "token", "private key", "api_key", "access_key", "authorization", "cookie"}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return strings.Contains(lower, "-----begin") || strings.Contains(lower, "base64")
}

func containsHiddenScriptExecutionFeatures(content string) bool {
	if content == "" {
		return false
	}
	lower := strings.ToLower(content)
	if strings.Contains(lower, "executionpolicy bypass") && strings.Contains(lower, "windowstyle hidden") {
		return true
	}
	return strings.Contains(lower, "windowstyle hidden") ||
		strings.Contains(lower, "executionpolicy bypass") ||
		strings.Contains(lower, "-encodedcommand") ||
		strings.Contains(lower, "-enc ")
}

func containsBase64EncodedPayload(content string) bool {
	return len(decodedBase64Payloads(content)) > 0
}

func containsHiddenScriptExecutionFeaturesInDecodedBase64(content string) bool {
	for _, decoded := range decodedBase64Payloads(content) {
		if containsHiddenScriptExecutionFeatures(strings.ToLower(decoded)) {
			return true
		}
	}
	return false
}

func decodedBase64Payloads(content string) []string {
	if content == "" {
		return nil
	}
	fields := strings.FieldsFunc(content, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', '"', '\'', '`', ',', ';', ':', '|', '(', ')', '[', ']', '{', '}', '=', '+':
			return true
		default:
			return false
		}
	})
	decodedPayloads := make([]string, 0)
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if len(trimmed) < 16 || len(trimmed)%4 != 0 {
			continue
		}
		if !looksLikeBase64Token(trimmed) {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err == nil {
			decodedPayloads = append(decodedPayloads, string(decoded))
		}
	}
	return decodedPayloads
}

func looksLikeBase64Token(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

func normalizeAgentGuardTarget(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" {
		return ""
	}

	if strings.HasPrefix(target, "/") {
		return path.Clean(target)
	}
	if !looksLikeWindowsPath(target) {
		return path.Clean(target)
	}

	target = strings.ReplaceAll(target, "/", `\`)
	lower := strings.ToLower(target)
	switch {
	case strings.HasPrefix(lower, `\\?\unc\`):
		target = `\\` + target[len(`\\?\UNC\`):]
	case strings.HasPrefix(lower, `\\?\`):
		target = target[len(`\\?\`):]
	}
	target = filepath.Clean(target)
	target = trimWindowsPathSegments(target)
	if looksLikeWindowsPath(target) {
		target = strings.ToLower(target)
	}
	return target
}

func trimWindowsPathSegments(path string) string {
	if path == "" {
		return ""
	}
	prefix := ""
	rest := path
	if strings.HasPrefix(rest, `\\`) {
		prefix = `\\`
		rest = strings.TrimPrefix(rest, `\\`)
	}
	parts := strings.Split(rest, `\`)
	for i, part := range parts {
		parts[i] = strings.TrimRight(part, " .")
	}
	return prefix + strings.Join(parts, `\`)
}

func looksLikeWindowsPath(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if len(path) >= 2 && path[1] == ':' {
		return true
	}
	return strings.Contains(path, `\`)
}

func decodeAgentGuardContent(encoding, content string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "plain", "text", "utf-8":
		return content, nil
	case "base64", "b64":
		if strings.TrimSpace(content) == "" {
			return "", nil
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return "", badRequest("content encoding base64 is invalid")
		}
		return string(decoded), nil
	default:
		return content, nil
	}
}

func decodedAgentGuardContentOrEmpty(encoding, content string) string {
	decoded, err := decodeAgentGuardContent(encoding, content)
	if err != nil {
		return content
	}
	return decoded
}

func hashAgentGuardPayload(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func buildAgentGuardExplanation(targetCategory, riskLevel, matchedRule, normalizedTarget string, resolution agentGuardTargetResolution, contentEncoding, rawContent, decodedContent string) *agentGuardExplanation {
	return &agentGuardExplanation{
		TargetCategory: strings.ToLower(strings.TrimSpace(targetCategory)),
		RiskLevel:      strings.ToLower(strings.TrimSpace(riskLevel)),
		MatchedRule:    strings.TrimSpace(matchedRule),
		Signals:        agentGuardExplanationSignals(targetCategory, riskLevel, normalizedTarget, resolution, contentEncoding, rawContent, decodedContent),
	}
}

func modelToolCallExplanation(explanation *agentGuardExplanation) *model.ToolCallExplanation {
	if explanation == nil {
		return nil
	}
	return &model.ToolCallExplanation{
		TargetCategory: explanation.TargetCategory,
		RiskLevel:      explanation.RiskLevel,
		MatchedRule:    explanation.MatchedRule,
		Signals:        append([]string(nil), explanation.Signals...),
	}
}

func agentGuardExplanationSignals(targetCategory, riskLevel, normalizedTarget string, resolution agentGuardTargetResolution, contentEncoding, rawContent, decodedContent string) []string {
	added := map[string]struct{}{}
	signals := make([]string, 0, 8)
	add := func(signal string) {
		signal = strings.TrimSpace(signal)
		if signal == "" {
			return
		}
		if _, exists := added[signal]; exists {
			return
		}
		added[signal] = struct{}{}
		signals = append(signals, signal)
	}

	pathCandidates := agentGuardTargetCategoryCandidates(normalizedTarget, resolution)
	if strings.EqualFold(strings.TrimSpace(targetCategory), "self_tamper") {
		add("AgentToolGate self-tamper target")
	}
	if agentGuardExplanationMatchesPath(pathCandidates, func(candidate string) bool {
		return agentGuardPathMatchesDirOrDescendant(candidate, `appdata/roaming/microsoft/windows/start menu/programs/startup`)
	}) {
		add("Windows Startup persistence path")
	}
	if agentGuardExplanationMatchesPath(pathCandidates, func(candidate string) bool {
		return agentGuardPathMatchesDirOrDescendant(candidate, `.ssh`)
	}) {
		add("SSH credential path")
	}
	if agentGuardExplanationMatchesPath(pathCandidates, func(candidate string) bool {
		return agentGuardPathMatchesDirOrDescendant(candidate, `.git/hooks`)
	}) {
		add("Git hooks persistence path")
	}

	if strings.EqualFold(strings.TrimSpace(contentEncoding), "base64") || strings.EqualFold(strings.TrimSpace(contentEncoding), "b64") || containsBase64EncodedPayload(strings.ToLower(rawContent)) {
		add("Base64-encoded script content")
	}

	scriptSignalSources := agentGuardSignalContentSources(rawContent, decodedContent)
	if agentGuardSignalContentContains(scriptSignalSources, "executionpolicy bypass") {
		add("PowerShell ExecutionPolicy Bypass")
	}
	if agentGuardSignalContentContains(scriptSignalSources, "windowstyle hidden") {
		add("PowerShell hidden window execution")
	}
	if riskLevelRank(riskLevel) >= riskLevelRank("high") {
		add("High-risk local action")
	}
	if strings.EqualFold(strings.TrimSpace(targetCategory), "workspace") && riskLevelRank(riskLevel) < riskLevelRank("high") {
		add("Workspace-scoped write")
	}
	return signals
}

func agentGuardExplanationMatchesPath(candidates []string, match func(string) bool) bool {
	for _, candidate := range candidates {
		normalized := normalizeAgentGuardTarget(candidate)
		if normalized == "" {
			continue
		}
		if match(normalized) {
			return true
		}
	}
	return false
}

func agentGuardSignalContentSources(rawContent, decodedContent string) []string {
	sources := make([]string, 0, 1+len(decodedBase64Payloads(rawContent)))
	if strings.TrimSpace(decodedContent) != "" {
		sources = append(sources, strings.ToLower(decodedContent))
	}
	for _, decoded := range decodedBase64Payloads(rawContent) {
		trimmed := strings.ToLower(strings.TrimSpace(decoded))
		if trimmed != "" {
			sources = append(sources, trimmed)
		}
	}
	return sources
}

func agentGuardSignalContentContains(sources []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return false
	}
	for _, source := range sources {
		if strings.Contains(source, needle) {
			return true
		}
	}
	return false
}

func redactAgentGuardInputForAudit(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return defaultJSON(value)
	}
	if obj, ok := decoded.(map[string]any); ok {
		if _, exists := obj["content"]; exists {
			obj["content"] = "[REDACTED]"
		}
		decoded = obj
	}
	redacted := redactJSONValueByValue(redactJSONValueByKey(decoded))
	raw, err := json.Marshal(redacted)
	if err != nil {
		return defaultJSON(value)
	}
	return raw
}

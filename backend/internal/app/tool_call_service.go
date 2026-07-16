package app

import (
	"context"
	"encoding/json"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/telemetry"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

func (a *App) createToolCall(ctx context.Context, reqCtx RequestContext, toolKey string, arguments json.RawMessage) (toolCallResponse, error) {
	ctx, requestSpan := telemetry.StartSpan(ctx, "agenttoolgate.request", attribute.String("tool.key", toolKey))
	defer requestSpan.End()

	_, authSpan := telemetry.StartSpan(ctx, "agenttoolgate.auth",
		attribute.String("workspace.id", reqCtx.Workspace.ID),
		attribute.String("user.role", reqCtx.User.Role),
	)
	authSpan.End()

	inputRedactedJSON := redactToolInputForAudit(toolKey, arguments)
	emptyExecutionJSON := json.RawMessage(`{}`)
	inputExecutionJSON := defaultJSON(arguments)

	tool, err := a.store.GetToolByKey(ctx, reqCtx.Workspace.ID, toolKey)
	if err != nil {
		return toolCallResponse{}, err
	}
	traceID := telemetry.TraceID(ctx)
	if traceID == "" {
		traceID = uuid.NewString()
	}
	requestID := "req_" + uuid.NewString()
	decodedArgs, _ := decodeJSONValue(arguments)
	effectiveRisk := a.deriveToolCallRisk(tool, decodedArgs)
	if !a.allowWorkspaceToolCall(reqCtx.Workspace.ID) {
		explanation := policyExplanationForAudit(tool, effectiveRisk, managedPolicyEvaluation{
			Decision:      policyAllow,
			Reason:        "rate limit exceeded",
			Defaulted:     true,
			ConnectorType: policyConnectorTypeForTool(tool),
			Resource:      extractPolicyResource(tool, decodedArgs),
		})
		_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "rate_limited"))
		call, callErr := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
			WorkspaceID:        reqCtx.Workspace.ID,
			RequestID:          requestID,
			ActorID:            reqCtx.User.ID,
			ActorSubject:       reqCtx.Identity.Subject,
			ActorEmail:         reqCtx.Identity.Email,
			ActorName:          reqCtx.Identity.Name,
			ToolID:             tool.ID,
			ToolKey:            tool.Key(),
			Status:             "rate_limited",
			RiskLevel:          effectiveRisk,
			PolicyDecision:     policyAllow,
			ApprovalID:         "",
			DurationMs:         0,
			InputRedactedJSON:  inputRedactedJSON,
			InputExecutionJSON: emptyExecutionJSON,
			OutputRedactedJSON: json.RawMessage(`{}`),
			Explanation:        explanation,
			ErrorMessage:       errRateLimited.Error(),
			TraceID:            traceID,
		})
		if callErr != nil {
			telemetry.RecordError(auditSpan, callErr)
			auditSpan.End()
			return toolCallResponse{}, callErr
		}
		auditSpan.End()
		telemetry.RecordToolCall(tool.Key(), "rate_limited", 0)
		return toolCallResponse{
			Status:  "rate_limited",
			CallID:  call.ID,
			TraceID: traceID,
			Message: errRateLimited.Error(),
		}, errRateLimited
	}
	_, policySpan := telemetry.StartSpan(ctx, "agenttoolgate.policy.evaluate",
		attribute.String("tool.namespace", tool.Namespace),
		attribute.String("tool.name", tool.Name),
		attribute.String("tool.operation_type", tool.OperationType),
	)
	defaultDecision := a.decidePolicyDetailed(tool, reqCtx.User.Role)
	policyDecision := string(defaultDecision.Effect)
	policyReason := defaultDecision.Reason
	policyRuleName := defaultDecision.RuleName
	explanation := policyExplanationForAudit(tool, effectiveRisk, managedPolicyEvaluation{
		Decision:      policyDecision,
		Reason:        policyReason,
		Defaulted:     true,
		ConnectorType: policyConnectorTypeForTool(tool),
		Resource:      extractPolicyResource(tool, decodedArgs),
	})
	if policyDecision != policyDeny {
		if validationErr := a.validateToolCallBeforePolicyResponse(ctx, reqCtx.Workspace.ID, tool, decodedArgs); validationErr != nil {
			telemetry.RecordError(policySpan, validationErr)
			policySpan.End()
			_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write")
			if _, callErr := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
				WorkspaceID:        reqCtx.Workspace.ID,
				RequestID:          requestID,
				ActorID:            reqCtx.User.ID,
				ActorSubject:       reqCtx.Identity.Subject,
				ActorEmail:         reqCtx.Identity.Email,
				ActorName:          reqCtx.Identity.Name,
				ToolID:             tool.ID,
				ToolKey:            tool.Key(),
				Status:             "failed",
				RiskLevel:          effectiveRisk,
				PolicyDecision:     policyDecision,
				ApprovalID:         "",
				DurationMs:         0,
				InputRedactedJSON:  inputRedactedJSON,
				InputExecutionJSON: emptyExecutionJSON,
				OutputRedactedJSON: json.RawMessage(`{}`),
				Explanation:        explanation,
				ErrorMessage:       validationErr.Error(),
				TraceID:            traceID,
			}); callErr != nil {
				telemetry.RecordError(auditSpan, callErr)
				auditSpan.End()
				return toolCallResponse{}, callErr
			}
			auditSpan.End()
			telemetry.RecordToolCall(tool.Key(), "failed", 0)
			return toolCallResponse{}, validationErr
		}
		policyDecision, policyReason = a.deriveToolCallPolicy(tool, decodedArgs, policyDecision, policyReason)
		if policyReason != defaultDecision.Reason {
			policyRuleName = "derived-tool-policy"
		}
	}
	policyEvaluation, evalErr := a.evaluateManagedPolicyForTool(ctx, reqCtx.Workspace.ID, tool, decodedArgs, effectiveRisk, policyDecision, policyReason, policyRuleName)
	if evalErr != nil {
		policySpan.End()
		return toolCallResponse{}, evalErr
	}
	policyDecision = policyEvaluation.Decision
	policyReason = policyEvaluation.Reason
	explanation = policyExplanationForAudit(tool, effectiveRisk, policyEvaluation)
	policySpan.SetAttributes(attribute.String("policy.decision", policyDecision))
	policySpan.End()

	switch policyDecision {
	case policyDeny:
		_, approvalSpan := telemetry.StartSpan(ctx, "agenttoolgate.approval.check", attribute.String("policy.decision", policyDecision))
		approvalSpan.End()
		_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", "denied"))
		call, callErr := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
			WorkspaceID:        reqCtx.Workspace.ID,
			RequestID:          requestID,
			ActorID:            reqCtx.User.ID,
			ActorSubject:       reqCtx.Identity.Subject,
			ActorEmail:         reqCtx.Identity.Email,
			ActorName:          reqCtx.Identity.Name,
			ToolID:             tool.ID,
			ToolKey:            tool.Key(),
			Status:             "denied",
			RiskLevel:          effectiveRisk,
			PolicyDecision:     policyDecision,
			ApprovalID:         "",
			DurationMs:         0,
			InputRedactedJSON:  inputRedactedJSON,
			InputExecutionJSON: emptyExecutionJSON,
			OutputRedactedJSON: json.RawMessage(`{}`),
			Explanation:        explanation,
			ErrorMessage:       policyReason,
			TraceID:            traceID,
		})
		if callErr != nil {
			telemetry.RecordError(auditSpan, callErr)
			auditSpan.End()
			return toolCallResponse{}, callErr
		}
		auditSpan.End()
		telemetry.RecordToolCall(tool.Key(), "denied", 0)
		return toolCallResponse{
			Status:     "denied",
			TraceID:    traceID,
			CallID:     call.ID,
			Reason:     policyReason,
			ApprovalID: "",
		}, nil

	case policyRequireApproval:
		_, approvalSpan := telemetry.StartSpan(ctx, "agenttoolgate.approval.check", attribute.String("policy.decision", policyDecision))
		approval, approvalErr := a.store.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
			WorkspaceID:     reqCtx.Workspace.ID,
			ToolKey:         tool.Key(),
			ToolDisplayName: tool.DisplayName,
			RequestedBy:     approvalRequestedBy(reqCtx.User.Name, reqCtx.Identity.Subject),
			Reason:          policyReason,
		})
		if approvalErr != nil {
			telemetry.RecordError(approvalSpan, approvalErr)
			approvalSpan.End()
			return toolCallResponse{}, approvalErr
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
			RiskLevel:          effectiveRisk,
			PolicyDecision:     policyDecision,
			ApprovalID:         approval.ID,
			DurationMs:         0,
			InputRedactedJSON:  inputRedactedJSON,
			InputExecutionJSON: inputExecutionJSON,
			OutputRedactedJSON: json.RawMessage(`{}`),
			Explanation:        explanation,
			ErrorMessage:       "",
			TraceID:            traceID,
		})
		if callErr != nil {
			telemetry.RecordError(auditSpan, callErr)
			auditSpan.End()
			return toolCallResponse{}, callErr
		}
		auditSpan.End()
		telemetry.RecordToolCall(tool.Key(), "approval_required", 0)
		if call.ApprovalStatus == "" {
			call.ApprovalStatus = approval.Status
		}
		a.publishApprovalEvent(approval)
		return toolCallResponse{
			Status:         "approval_required",
			TraceID:        traceID,
			CallID:         call.ID,
			ApprovalID:     approval.ID,
			ApprovalStatus: approval.Status,
			Message:        "This tool call requires approval.",
			Reason:         policyReason,
		}, nil
	}

	_, approvalSpan := telemetry.StartSpan(ctx, "agenttoolgate.approval.check", attribute.String("policy.decision", policyDecision))
	approvalSpan.End()

	start := time.Now().UTC()
	connectorCtx, connectorSpan := telemetry.StartSpan(ctx, "agenttoolgate.connector.execute", attribute.String("tool.key", tool.Key()))
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

	_, auditSpan := telemetry.StartSpan(ctx, "agenttoolgate.audit.write", attribute.String("tool_call.status", status))
	call, callErr := a.store.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        reqCtx.Workspace.ID,
		RequestID:          requestID,
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.Identity.Name,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             status,
		RiskLevel:          effectiveRisk,
		PolicyDecision:     policyDecision,
		ApprovalID:         "",
		DurationMs:         durationMs,
		InputRedactedJSON:  inputRedactedJSON,
		InputExecutionJSON: emptyExecutionJSON,
		OutputRedactedJSON: redactToolOutputForAudit(tool.Key(), resultJSON),
		Explanation:        explanation,
		ErrorMessage:       errorMessage,
		TraceID:            traceID,
	})
	if callErr != nil {
		telemetry.RecordError(auditSpan, callErr)
		auditSpan.End()
		return toolCallResponse{}, callErr
	}
	auditSpan.End()
	telemetry.RecordToolCall(tool.Key(), status, time.Duration(durationMs)*time.Millisecond)

	if execErr != nil {
		return toolCallResponse{}, execErr
	}

	return toolCallResponse{
		Status:  "success",
		Result:  resultPayload,
		CallID:  call.ID,
		TraceID: traceID,
	}, nil
}

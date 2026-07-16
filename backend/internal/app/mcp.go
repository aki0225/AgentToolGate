package app

import (
	"context"
	"errors"
	"net/http"

	"agenttoolgate/backend/internal/mcp"
)

func (a *App) MCPHandler() http.Handler {
	return a.newMCPHandler()
}

func (a *App) MCPStreamableHTTPHandler() http.Handler {
	return a.newMCPHandler().StreamableHTTPHandler()
}

func (a *App) newMCPHandler() *mcp.Handler {
	return mcp.NewHandler(mcp.Config{
		Store: a.store,
		ResolveContext: func(ctx context.Context) (mcp.RequestContext, error) {
			reqCtx, ok := requestContextFrom(ctx)
			if !ok {
				return mcp.RequestContext{}, errors.New("missing request context")
			}
			if err := requireViewTools(reqCtx); err != nil {
				return mcp.RequestContext{}, err
			}
			return mcp.RequestContext{WorkspaceID: reqCtx.Workspace.ID}, nil
		},
		ResolveSessionContext: func(ctx context.Context) (mcp.RequestContext, error) {
			reqCtx, ok := requestContextFrom(ctx)
			if !ok {
				return mcp.RequestContext{}, errors.New("missing request context")
			}
			if err := requireExecuteTools(reqCtx); err != nil {
				return mcp.RequestContext{}, err
			}
			return mcp.RequestContext{WorkspaceID: reqCtx.Workspace.ID}, nil
		},
		CallTool: func(ctx context.Context, req mcp.ToolCallRequest) (mcp.ToolCallResult, error) {
			reqCtx, ok := requestContextFrom(ctx)
			if !ok {
				return mcp.ToolCallResult{}, errors.New("missing request context")
			}
			if err := requireExecuteTools(reqCtx); err != nil {
				return mcp.ToolCallResult{}, err
			}
			result, err := a.createToolCall(ctx, reqCtx, req.Tool, req.Arguments)
			if err != nil {
				return mcp.ToolCallResult{}, err
			}
			return mcp.ToolCallResult{
				Status:         result.Status,
				Result:         result.Result,
				CallID:         result.CallID,
				TraceID:        result.TraceID,
				Message:        result.Message,
				Reason:         result.Reason,
				ApprovalID:     result.ApprovalID,
				ApprovalStatus: result.ApprovalStatus,
			}, nil
		},
		Logger: a.logger,
	})
}

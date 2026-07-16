package app

import (
	"errors"
	"net/http"
	"strings"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/go-chi/chi/v5"
)

func (a *App) handlePatchTool(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireManageTools(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	toolID := chi.URLParam(r, "id")
	if strings.TrimSpace(toolID) == "" {
		a.respondError(w, badRequest("tool id is required"))
		return
	}

	var req updateToolRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	if req.Enabled == nil {
		a.respondError(w, badRequest("enabled is required"))
		return
	}

	tool, err := a.store.UpdateTool(r.Context(), reqCtx.Workspace.ID, toolID, model.UpdateToolInput{Enabled: req.Enabled})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.respondError(w, err)
			return
		}
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tool)
}

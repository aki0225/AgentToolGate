package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"agenttoolgate/backend/internal/model"
)

const approvalSSEHeartbeatInterval = 30 * time.Second

type approvalSSEHub struct {
	mu      sync.RWMutex
	clients map[string]map[*approvalSSEClient]struct{}
	logger  *slog.Logger
}

type approvalSSEClient struct {
	ch chan approvalSSEMessage
}

type approvalSSEMessage struct {
	Event string
	Data  []byte
}

type approvalSSEPayload struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ToolKey   string    `json:"toolKey"`
	CreatedAt time.Time `json:"createdAt"`
}

func newApprovalSSEHub(logger *slog.Logger) *approvalSSEHub {
	if logger == nil {
		logger = slog.Default()
	}
	return &approvalSSEHub{
		clients: map[string]map[*approvalSSEClient]struct{}{},
		logger:  logger,
	}
}

func (a *App) handleApprovalStream(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireViewApprovals(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}
	if a.approvalHub == nil {
		a.respondError(w, fmt.Errorf("approval stream is not configured"))
		return
	}
	a.approvalHub.serveHTTP(w, r, reqCtx.Workspace.ID)
}

func (a *App) publishApprovalEvent(approval model.ApprovalRequest) {
	if a.approvalHub == nil {
		return
	}
	a.approvalHub.publishApproval(approval)
}

func (h *approvalSSEHub) serveHTTP(w http.ResponseWriter, r *http.Request, workspaceID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	client := &approvalSSEClient{ch: make(chan approvalSSEMessage, 16)}
	h.register(workspaceID, client)
	defer h.unregister(workspaceID, client)

	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		h.logger.Warn("write approval sse comment failed", "error", err)
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(approvalSSEHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case message := <-client.ch:
			if err := writeSSEMessage(w, message); err != nil {
				h.logger.Warn("write approval sse event failed", "error", err)
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				h.logger.Warn("write approval sse heartbeat failed", "error", err)
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (h *approvalSSEHub) publishApproval(approval model.ApprovalRequest) {
	payload, err := json.Marshal(approvalSSEPayload{
		ID:        approval.ID,
		Status:    approval.Status,
		ToolKey:   approval.ToolKey,
		CreatedAt: approval.CreatedAt,
	})
	if err != nil {
		h.logger.Warn("encode approval sse payload failed", "approval_id", approval.ID, "error", err)
		return
	}

	message := approvalSSEMessage{
		Event: "approval",
		Data:  payload,
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	clients := h.clients[approval.WorkspaceID]
	for client := range clients {
		select {
		case client.ch <- message:
		default:
			h.logger.Warn("drop slow approval sse client", "workspace_id", approval.WorkspaceID, "approval_id", approval.ID)
		}
	}
}

func (h *approvalSSEHub) register(workspaceID string, client *approvalSSEClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[workspaceID]; !ok {
		h.clients[workspaceID] = map[*approvalSSEClient]struct{}{}
	}
	h.clients[workspaceID][client] = struct{}{}
}

func (h *approvalSSEHub) unregister(workspaceID string, client *approvalSSEClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients, ok := h.clients[workspaceID]
	if !ok {
		return
	}
	delete(clients, client)
	if len(clients) == 0 {
		delete(h.clients, workspaceID)
	}
}

func writeSSEMessage(w http.ResponseWriter, message approvalSSEMessage) error {
	if message.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", message.Event); err != nil {
			return err
		}
	}
	for _, line := range splitSSELines(message.Data) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}

func splitSSELines(data []byte) []string {
	if len(data) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, 1)
	start := 0
	for start < len(data) {
		end := start
		for end < len(data) && data[end] != '\n' {
			end++
		}
		line := string(data[start:end])
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
		start = end + 1
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

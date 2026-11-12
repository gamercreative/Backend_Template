// Package handlers translates HTTP to service calls and back. No business
// rules and no SQL here: decode, call one service method, encode.
//
// Shared helpers in this file are owned by the orchestrator. Each domain file
// (agents.go, conversations.go, chat.go) owns its register* method and handlers.
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	httpx "alphahack/internal/http"
	"alphahack/internal/services"
)

type Deps struct {
	Agents        services.AgentService
	Conversations services.ConversationService
	Chat          services.ChatService
	Health        services.HealthService
	Logger        *slog.Logger
}

type Handlers struct {
	agents        services.AgentService
	conversations services.ConversationService
	chat          services.ChatService
	health        services.HealthService
	log           *slog.Logger
}

func New(d Deps) *Handlers {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{
		agents:        d.Agents,
		conversations: d.Conversations,
		chat:          d.Chat,
		health:        d.Health,
		log:           log,
	}
}

// Register mounts every route on mux (Go 1.22 "METHOD /path/{param}" patterns).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	h.registerAgents(mux)
	h.registerConversations(mux)
	h.registerChat(mux)
}

func (h *Handlers) healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.health.Check(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- shared helpers --------------------------------------------------------

// list is the envelope for every collection response.
type list[T any] struct {
	Data   []T `json:"data"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// writeList writes a collection; a nil slice is sent as [].
func writeList[T any](w http.ResponseWriter, items []T, p services.Page) {
	if items == nil {
		items = []T{}
	}
	p = p.Normalize()
	httpx.JSON(w, http.StatusOK, list[T]{Data: items, Limit: p.Limit, Offset: p.Offset})
}

// decode reads the JSON body into dst. On failure it writes the 400 and
// returns false; the caller just returns.
func (h *Handlers) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return false
	}
	return true
}

// page parses ?limit=&offset=. On a malformed value it writes the 400 and
// returns false.
func (h *Handlers) page(w http.ResponseWriter, r *http.Request) (services.Page, bool) {
	var p services.Page
	for name, dst := range map[string]*int{"limit": &p.Limit, "offset": &p.Offset} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeBadRequest, name+" must be a non-negative integer")
			return p, false
		}
		*dst = n
	}
	return p.Normalize(), true
}

// fail maps any error from a service to the one JSON error format.
func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, body := h.errorBody(r, err)
	httpx.JSON(w, status, body)
}

// errorBody classifies err into an HTTP status and the standard error body,
// logging server-side failures. Use it directly only where a normal response
// can no longer be written (e.g. as the "error" event of an SSE stream).
func (h *Handlers) errorBody(r *http.Request, err error) (int, httpx.ErrorBody) {
	rid := httpx.RequestID(r.Context())
	mk := func(status int, code, message string) (int, httpx.ErrorBody) {
		return status, httpx.ErrorBody{Error: httpx.ErrorDetail{Code: code, Message: message, RequestID: rid}}
	}
	var se *services.Error
	switch {
	case errors.As(err, &se):
		status := statusFor(se.Code)
		if status >= 500 {
			h.log.ErrorContext(r.Context(), "request failed", "code", se.Code, "err", se.Error(), "request_id", rid)
		}
		return mk(status, se.Code, se.Message)
	case errors.Is(err, context.DeadlineExceeded):
		return mk(http.StatusGatewayTimeout, httpx.CodeTimeout, "the request timed out")
	case errors.Is(err, context.Canceled):
		// Client went away; 499 is the conventional status, nobody reads the body.
		return mk(499, httpx.CodeTimeout, "the request was cancelled")
	default:
		h.log.ErrorContext(r.Context(), "unclassified error", "err", err.Error(), "request_id", rid)
		return mk(http.StatusInternalServerError, httpx.CodeInternal, "internal error")
	}
}

func statusFor(code string) int {
	switch code {
	case services.CodeInvalid:
		return http.StatusBadRequest
	case services.CodeNotFound:
		return http.StatusNotFound
	case services.CodeConflict:
		return http.StatusConflict
	case services.CodeUpstream:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

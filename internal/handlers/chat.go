package handlers

import (
	"net/http"

	httpx "alphahack/internal/http"
	"alphahack/internal/services"
)

// registerChat mounts the chat routes: sending a message, the runs of a
// conversation, one run with its tool-call trace, and the tool catalogue.
func (h *Handlers) registerChat(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/conversations/{id}/messages", h.sendMessage)
	mux.HandleFunc("POST /v1/conversations/{id}/messages/stream", h.streamMessage)
	mux.HandleFunc("GET /v1/conversations/{id}/runs", h.listConversationRuns)
	mux.HandleFunc("GET /v1/runs/{id}", h.getRun)
	mux.HandleFunc("GET /v1/tools", h.listTools)
}

func (h *Handlers) sendMessage(w http.ResponseWriter, r *http.Request) {
	var in services.SendMessageInput
	if !h.decode(w, r, &in) {
		return
	}
	res, err := h.chat.SendMessage(r.Context(), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

// streamMessage is sendMessage with server-sent events. The stream is opened
// on the first event, so a failure before any event is still a plain JSON
// error. Events: the service's own event types, then "done" with the
// ChatResult, or "error" with the standard error body (no "done" after it).
func (h *Handlers) streamMessage(w http.ResponseWriter, r *http.Request) {
	var in services.SendMessageInput
	if !h.decode(w, r, &in) {
		return
	}
	var sse *httpx.SSEWriter
	open := func() *httpx.SSEWriter {
		if sse == nil {
			sse = httpx.NewSSE(w)
		}
		return sse
	}
	emit := func(ev services.ChatEvent) {
		// A failed Send means the client is gone; the service sees that through the context.
		_ = open().Send(ev.Type, ev.Data)
	}
	res, err := h.chat.StreamMessage(r.Context(), r.PathValue("id"), in, emit)
	if err != nil {
		if sse == nil {
			h.fail(w, r, err)
			return
		}
		_, body := h.errorBody(r, err)
		_ = sse.Send("error", body)
		return
	}
	_ = open().Send("done", res)
}

func (h *Handlers) listConversationRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	runs, err := h.chat.ListRuns(r.Context(), r.PathValue("id"), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeList(w, runs, p)
}

func (h *Handlers) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.chat.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

// toolsResponse is the bare {"data": [...]} envelope of GET /v1/tools (no
// pagination fields).
type toolsResponse struct {
	Data []services.ToolInfo `json:"data"`
}

func (h *Handlers) listTools(w http.ResponseWriter, r *http.Request) {
	tools := h.chat.ListTools()
	if tools == nil {
		tools = []services.ToolInfo{}
	}
	httpx.JSON(w, http.StatusOK, toolsResponse{Data: tools})
}

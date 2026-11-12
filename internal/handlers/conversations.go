package handlers

import (
	"net/http"

	httpx "alphahack/internal/http"
	"alphahack/internal/services"
)

// registerConversations mounts the conversations routes. The chat routes
// (messages, runs) are registered by chat.go.
func (h *Handlers) registerConversations(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/conversations", h.createConversation)
	mux.HandleFunc("GET /v1/conversations", h.listConversations)
	mux.HandleFunc("GET /v1/conversations/{id}", h.getConversation)
	mux.HandleFunc("DELETE /v1/conversations/{id}", h.deleteConversation)
	mux.HandleFunc("GET /v1/conversations/{id}/messages", h.listConversationMessages)
}

func (h *Handlers) createConversation(w http.ResponseWriter, r *http.Request) {
	var in services.CreateConversationInput
	if !h.decode(w, r, &in) {
		return
	}
	conv, err := h.conversations.Create(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, conv)
}

func (h *Handlers) listConversations(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	convs, err := h.conversations.List(r.Context(), r.URL.Query().Get("agent_id"), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeList(w, convs, p)
}

func (h *Handlers) getConversation(w http.ResponseWriter, r *http.Request) {
	conv, err := h.conversations.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, conv)
}

func (h *Handlers) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if err := h.conversations.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

func (h *Handlers) listConversationMessages(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	msgs, err := h.conversations.ListMessages(r.Context(), r.PathValue("id"), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeList(w, msgs, p)
}

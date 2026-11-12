package handlers

import (
	"net/http"

	httpx "alphahack/internal/http"
	"alphahack/internal/services"
)

// registerAgents mounts the agents routes.
func (h *Handlers) registerAgents(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/agents", h.createAgent)
	mux.HandleFunc("GET /v1/agents", h.listAgents)
	mux.HandleFunc("GET /v1/agents/{id}", h.getAgent)
	mux.HandleFunc("PATCH /v1/agents/{id}", h.updateAgent)
	mux.HandleFunc("DELETE /v1/agents/{id}", h.deleteAgent)
}

func (h *Handlers) createAgent(w http.ResponseWriter, r *http.Request) {
	var in services.CreateAgentInput
	if !h.decode(w, r, &in) {
		return
	}
	agent, err := h.agents.Create(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, agent)
}

func (h *Handlers) listAgents(w http.ResponseWriter, r *http.Request) {
	p, ok := h.page(w, r)
	if !ok {
		return
	}
	agents, err := h.agents.List(r.Context(), p)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeList(w, agents, p)
}

func (h *Handlers) getAgent(w http.ResponseWriter, r *http.Request) {
	agent, err := h.agents.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agent)
}

func (h *Handlers) updateAgent(w http.ResponseWriter, r *http.Request) {
	var in services.UpdateAgentInput
	if !h.decode(w, r, &in) {
		return
	}
	agent, err := h.agents.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, agent)
}

func (h *Handlers) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := h.agents.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

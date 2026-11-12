package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	httpx "alphahack/internal/http"
	"alphahack/internal/queries"
	"alphahack/internal/services"
)

// agentServiceFake records what the handler passed in and returns canned results.
type agentServiceFake struct {
	calls int

	createIn  services.CreateAgentInput
	createOut queries.Agent
	createErr error

	getID  string
	getOut queries.Agent
	getErr error

	listPage services.Page
	listOut  []queries.Agent
	listErr  error

	updateID  string
	updateIn  services.UpdateAgentInput
	updateOut queries.Agent
	updateErr error

	deleteID  string
	deleteErr error
}

var _ services.AgentService = (*agentServiceFake)(nil)

func (f *agentServiceFake) Create(_ context.Context, in services.CreateAgentInput) (queries.Agent, error) {
	f.calls++
	f.createIn = in
	return f.createOut, f.createErr
}

func (f *agentServiceFake) Get(_ context.Context, id string) (queries.Agent, error) {
	f.calls++
	f.getID = id
	return f.getOut, f.getErr
}

func (f *agentServiceFake) List(_ context.Context, p services.Page) ([]queries.Agent, error) {
	f.calls++
	f.listPage = p
	return f.listOut, f.listErr
}

func (f *agentServiceFake) Update(_ context.Context, id string, in services.UpdateAgentInput) (queries.Agent, error) {
	f.calls++
	f.updateID = id
	f.updateIn = in
	return f.updateOut, f.updateErr
}

func (f *agentServiceFake) Delete(_ context.Context, id string) error {
	f.calls++
	f.deleteID = id
	return f.deleteErr
}

var agentFixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func agentSample(id string) queries.Agent {
	return queries.Agent{
		ID:           id,
		Name:         "Support bot",
		SystemPrompt: "Be brief.",
		Model:        "claude-test",
		CreatedAt:    agentFixedTime,
		UpdatedAt:    agentFixedTime,
	}
}

// agentServer mounts the handlers exactly as the server does.
func agentServer(t *testing.T, fake *agentServiceFake) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(Deps{Agents: fake}).Register(mux)
	return httpx.NewRouter(mux)
}

func agentDo(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func agentErrorCode(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, rec.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

func agentAssertJSONAgent(t *testing.T, rec *httptest.ResponseRecorder, want queries.Agent) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got queries.Agent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not an agent: %v: %s", err, rec.Body.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agent = %+v, want %+v", got, want)
	}
	// The wire format is snake_case.
	for _, key := range []string{`"system_prompt"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("body %s lacks key %s", rec.Body.String(), key)
		}
	}
}

// ---- POST /v1/agents -------------------------------------------------------

func TestAgentCreate(t *testing.T) {
	const body = `{"name":"Support bot","system_prompt":"Be brief.","model":"claude-test"}`
	want := services.CreateAgentInput{Name: "Support bot", SystemPrompt: "Be brief.", Model: "claude-test"}

	t.Run("created", func(t *testing.T) {
		fake := &agentServiceFake{createOut: agentSample("agt_1")}
		rec := agentDo(t, agentServer(t, fake), http.MethodPost, "/v1/agents", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
		}
		agentAssertJSONAgent(t, rec, agentSample("agt_1"))
		if fake.createIn != want {
			t.Errorf("service input = %+v, want %+v", fake.createIn, want)
		}
		if fake.calls != 1 {
			t.Errorf("service calls = %d, want 1", fake.calls)
		}
	})

	t.Run("service errors", func(t *testing.T) {
		tests := []struct {
			name       string
			err        error
			wantStatus int
			wantCode   string
			wantMsg    string
		}{
			{"validation", services.Invalid("name is required"), http.StatusBadRequest, "validation_failed", "name is required"},
			{"internal hides wrapped error", services.Internal(errors.New("pq: secret connection string")), http.StatusInternalServerError, "internal_error", "internal error"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				fake := &agentServiceFake{createErr: tt.err}
				rec := agentDo(t, agentServer(t, fake), http.MethodPost, "/v1/agents", body)
				if rec.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				code, msg := agentErrorCode(t, rec)
				if code != tt.wantCode || msg != tt.wantMsg {
					t.Errorf("error = %s/%s, want %s/%s", code, msg, tt.wantCode, tt.wantMsg)
				}
				if strings.Contains(rec.Body.String(), "secret") {
					t.Errorf("internal detail leaked: %s", rec.Body.String())
				}
			})
		}
	})

	t.Run("bad body is 400 and never reaches the service", func(t *testing.T) {
		tests := []struct {
			name string
			body string
		}{
			{"malformed json", `{"name":`},
			{"unknown field", `{"name":"x","temperature":1}`},
			{"wrong type", `{"name":42}`},
			{"empty body", ``},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				fake := &agentServiceFake{}
				rec := agentDo(t, agentServer(t, fake), http.MethodPost, "/v1/agents", tt.body)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
				}
				if code, _ := agentErrorCode(t, rec); code != httpx.CodeBadRequest {
					t.Errorf("code = %s, want bad_request", code)
				}
				if fake.calls != 0 {
					t.Errorf("service called %d times, want 0", fake.calls)
				}
			})
		}
	})
}

// ---- GET /v1/agents --------------------------------------------------------

func TestAgentList(t *testing.T) {
	t.Run("page of agents", func(t *testing.T) {
		fake := &agentServiceFake{listOut: []queries.Agent{agentSample("agt_1"), agentSample("agt_2")}}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents?limit=10&offset=20", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Data   []queries.Agent `json:"data"`
			Limit  int             `json:"limit"`
			Offset int             `json:"offset"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Data) != 2 || got.Data[0].ID != "agt_1" || got.Data[1].ID != "agt_2" {
			t.Errorf("data = %+v", got.Data)
		}
		if got.Limit != 10 || got.Offset != 20 {
			t.Errorf("limit/offset = %d/%d, want 10/20", got.Limit, got.Offset)
		}
		if want := (services.Page{Limit: 10, Offset: 20}); fake.listPage != want {
			t.Errorf("service page = %+v, want %+v", fake.listPage, want)
		}
	})

	t.Run("defaults are applied to the envelope", func(t *testing.T) {
		fake := &agentServiceFake{listOut: []queries.Agent{}}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"limit":50`) {
			t.Errorf("body %s lacks default limit 50", rec.Body.String())
		}
	})

	t.Run("empty list renders data as []", func(t *testing.T) {
		fake := &agentServiceFake{listOut: nil}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"data":[]`) {
			t.Errorf("body %s lacks \"data\":[]", rec.Body.String())
		}
	})

	t.Run("bad query params are 400 and skip the service", func(t *testing.T) {
		for _, target := range []string{"/v1/agents?limit=abc", "/v1/agents?offset=-1"} {
			fake := &agentServiceFake{}
			rec := agentDo(t, agentServer(t, fake), http.MethodGet, target, "")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", target, rec.Code)
				continue
			}
			if code, _ := agentErrorCode(t, rec); code != httpx.CodeBadRequest {
				t.Errorf("%s: code = %s, want bad_request", target, code)
			}
			if fake.calls != 0 {
				t.Errorf("%s: service called", target)
			}
		}
	})

	t.Run("service error", func(t *testing.T) {
		fake := &agentServiceFake{listErr: services.Internal(errors.New("boom"))}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents", "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("internal detail leaked: %s", rec.Body.String())
		}
	})
}

// ---- GET /v1/agents/{id} ---------------------------------------------------

func TestAgentGet(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		fake := &agentServiceFake{getOut: agentSample("agt_1")}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents/agt_1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
		agentAssertJSONAgent(t, rec, agentSample("agt_1"))
		if fake.getID != "agt_1" {
			t.Errorf("service id = %q, want agt_1", fake.getID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		fake := &agentServiceFake{getErr: services.NotFound("agent")}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents/missing", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		code, msg := agentErrorCode(t, rec)
		if code != "not_found" || msg != "agent not found" {
			t.Errorf("error = %s/%s", code, msg)
		}
	})

	t.Run("internal error does not leak", func(t *testing.T) {
		fake := &agentServiceFake{getErr: services.Internal(errors.New("dial tcp 10.0.0.1: refused"))}
		rec := agentDo(t, agentServer(t, fake), http.MethodGet, "/v1/agents/agt_1", "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		code, msg := agentErrorCode(t, rec)
		if code != "internal_error" || msg != "internal error" {
			t.Errorf("error = %s/%s", code, msg)
		}
		if strings.Contains(rec.Body.String(), "10.0.0.1") {
			t.Errorf("internal detail leaked: %s", rec.Body.String())
		}
	})
}

// ---- PATCH /v1/agents/{id} -------------------------------------------------

func TestAgentUpdate(t *testing.T) {
	t.Run("only present fields are sent", func(t *testing.T) {
		fake := &agentServiceFake{updateOut: agentSample("agt_1")}
		rec := agentDo(t, agentServer(t, fake), http.MethodPatch, "/v1/agents/agt_1", `{"model":"claude-next"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
		agentAssertJSONAgent(t, rec, agentSample("agt_1"))
		if fake.updateID != "agt_1" {
			t.Errorf("service id = %q, want agt_1", fake.updateID)
		}
		model := "claude-next"
		if fake.updateIn.Model == nil || *fake.updateIn.Model != model {
			t.Errorf("model = %v, want %q", fake.updateIn.Model, model)
		}
		if fake.updateIn.Name != nil || fake.updateIn.SystemPrompt != nil {
			t.Errorf("absent fields must stay nil: %+v", fake.updateIn)
		}
	})

	t.Run("explicit empty string is distinct from absent", func(t *testing.T) {
		fake := &agentServiceFake{updateOut: agentSample("agt_1")}
		rec := agentDo(t, agentServer(t, fake), http.MethodPatch, "/v1/agents/agt_1", `{"system_prompt":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if fake.updateIn.SystemPrompt == nil || *fake.updateIn.SystemPrompt != "" {
			t.Errorf("system_prompt = %v, want pointer to empty string", fake.updateIn.SystemPrompt)
		}
	})

	t.Run("not found and validation errors", func(t *testing.T) {
		tests := []struct {
			name       string
			err        error
			wantStatus int
			wantCode   string
		}{
			{"not found", services.NotFound("agent"), http.StatusNotFound, "not_found"},
			{"validation", services.Invalid("name must not be empty"), http.StatusBadRequest, "validation_failed"},
			{"internal", services.Internal(errors.New("secret")), http.StatusInternalServerError, "internal_error"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				fake := &agentServiceFake{updateErr: tt.err}
				rec := agentDo(t, agentServer(t, fake), http.MethodPatch, "/v1/agents/agt_1", `{"name":""}`)
				if rec.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				if code, _ := agentErrorCode(t, rec); code != tt.wantCode {
					t.Errorf("code = %s, want %s", code, tt.wantCode)
				}
				if strings.Contains(rec.Body.String(), "secret") {
					t.Errorf("internal detail leaked: %s", rec.Body.String())
				}
			})
		}
	})

	t.Run("bad body is 400 and never reaches the service", func(t *testing.T) {
		for _, body := range []string{`{"nme":"typo"}`, `{"name":`, `{"name":true}`} {
			fake := &agentServiceFake{}
			rec := agentDo(t, agentServer(t, fake), http.MethodPatch, "/v1/agents/agt_1", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", body, rec.Code)
				continue
			}
			if code, _ := agentErrorCode(t, rec); code != httpx.CodeBadRequest {
				t.Errorf("%s: code = %s, want bad_request", body, code)
			}
			if fake.calls != 0 {
				t.Errorf("%s: service called", body)
			}
		}
	})
}

// ---- DELETE /v1/agents/{id} ------------------------------------------------

func TestAgentDelete(t *testing.T) {
	t.Run("no content", func(t *testing.T) {
		fake := &agentServiceFake{}
		rec := agentDo(t, agentServer(t, fake), http.MethodDelete, "/v1/agents/agt_1", "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("204 must have no body, got %q", rec.Body.String())
		}
		if fake.deleteID != "agt_1" {
			t.Errorf("service id = %q, want agt_1", fake.deleteID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		fake := &agentServiceFake{deleteErr: services.NotFound("agent")}
		rec := agentDo(t, agentServer(t, fake), http.MethodDelete, "/v1/agents/missing", "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if code, _ := agentErrorCode(t, rec); code != "not_found" {
			t.Errorf("code = %s", code)
		}
	})

	t.Run("internal error does not leak", func(t *testing.T) {
		fake := &agentServiceFake{deleteErr: services.Internal(errors.New("fk violation on messages"))}
		rec := agentDo(t, agentServer(t, fake), http.MethodDelete, "/v1/agents/agt_1", "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "fk violation") {
			t.Errorf("internal detail leaked: %s", rec.Body.String())
		}
	})
}

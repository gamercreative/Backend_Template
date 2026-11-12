package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpx "alphahack/internal/http"
	"alphahack/internal/queries"
	"alphahack/internal/services"
)

// conversationServiceFake is an in-memory services.ConversationService that
// records what the handler passed in. err, when set, is returned by every method.
type conversationServiceFake struct {
	err   error
	calls int

	createIn  services.CreateConversationInput
	getID     string
	listAgent string
	listPage  services.Page
	deleteID  string
	msgID     string
	msgPage   services.Page

	conv     queries.Conversation
	convs    []queries.Conversation
	messages []queries.Message
}

func (f *conversationServiceFake) Create(_ context.Context, in services.CreateConversationInput) (queries.Conversation, error) {
	f.calls++
	f.createIn = in
	return f.conv, f.err
}

func (f *conversationServiceFake) Get(_ context.Context, id string) (queries.Conversation, error) {
	f.calls++
	f.getID = id
	return f.conv, f.err
}

func (f *conversationServiceFake) List(_ context.Context, agentID string, p services.Page) ([]queries.Conversation, error) {
	f.calls++
	f.listAgent = agentID
	f.listPage = p
	return f.convs, f.err
}

func (f *conversationServiceFake) Delete(_ context.Context, id string) error {
	f.calls++
	f.deleteID = id
	return f.err
}

func (f *conversationServiceFake) ListMessages(_ context.Context, id string, p services.Page) ([]queries.Message, error) {
	f.calls++
	f.msgID = id
	f.msgPage = p
	return f.messages, f.err
}

// conversationServe runs one request through the real router and mux.
func conversationServe(t *testing.T, fake *conversationServiceFake, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New(Deps{Conversations: fake}).Register(mux)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	httpx.NewRouter(mux).ServeHTTP(rec, req)
	return rec
}

func conversationErrorBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorDetail {
	t.Helper()
	var body httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error
}

func conversationFixture() queries.Conversation {
	return queries.Conversation{ID: "c1", AgentID: "a1", Title: "hello"}
}

func TestConversationCreate(t *testing.T) {
	fake := &conversationServiceFake{conv: conversationFixture()}
	rec := conversationServe(t, fake, http.MethodPost, "/v1/conversations",
		`{"agent_id":"a1","title":"hello"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	if fake.createIn != (services.CreateConversationInput{AgentID: "a1", Title: "hello"}) {
		t.Fatalf("service input = %+v", fake.createIn)
	}
	var got queries.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "c1" || got.AgentID != "a1" || got.Title != "hello" {
		t.Fatalf("body = %+v", got)
	}
}

func TestConversationCreateMalformedJSON(t *testing.T) {
	for _, body := range []string{`{"agent_id":`, `[]`, `{"agent_id":"a1"} trailing`} {
		fake := &conversationServiceFake{}
		rec := conversationServe(t, fake, http.MethodPost, "/v1/conversations", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
		if e := conversationErrorBody(t, rec); e.Code != httpx.CodeBadRequest {
			t.Fatalf("body %q: code = %q, want bad_request", body, e.Code)
		}
		if fake.calls != 0 {
			t.Fatalf("body %q: service called %d times", body, fake.calls)
		}
	}
}

func TestConversationServiceErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		code     string
		message  string
		leakText string
	}{
		{
			name:    "validation",
			err:     services.Invalid("agent_id is required"),
			status:  http.StatusBadRequest,
			code:    services.CodeInvalid,
			message: "agent_id is required",
		},
		{
			name:    "not found",
			err:     services.NotFound("conversation"),
			status:  http.StatusNotFound,
			code:    services.CodeNotFound,
			message: "conversation not found",
		},
		{
			name:     "internal hides wrapped error",
			err:      services.Internal(errors.New("pq: secret detail")),
			status:   http.StatusInternalServerError,
			code:     services.CodeInternal,
			message:  "internal error",
			leakText: "secret detail",
		},
		{
			name:     "plain error hides message",
			err:      errors.New("boom: secret detail"),
			status:   http.StatusInternalServerError,
			code:     httpx.CodeInternal,
			message:  "internal error",
			leakText: "secret detail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &conversationServiceFake{err: tc.err}
			rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/c1", "")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			e := conversationErrorBody(t, rec)
			if e.Code != tc.code || e.Message != tc.message {
				t.Fatalf("error = %+v, want code %q message %q", e, tc.code, tc.message)
			}
			if tc.leakText != "" && strings.Contains(rec.Body.String(), tc.leakText) {
				t.Fatalf("response leaks %q: %s", tc.leakText, rec.Body.String())
			}
		})
	}
}

func TestConversationCreateServiceErrorMapping(t *testing.T) {
	fake := &conversationServiceFake{err: services.Invalid("title too long")}
	rec := conversationServe(t, fake, http.MethodPost, "/v1/conversations", `{"agent_id":"a1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if e := conversationErrorBody(t, rec); e.Code != "validation_failed" {
		t.Fatalf("code = %q, want validation_failed", e.Code)
	}
}

func TestConversationGet(t *testing.T) {
	fake := &conversationServiceFake{conv: conversationFixture()}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/c1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if fake.getID != "c1" {
		t.Fatalf("service id = %q, want c1", fake.getID)
	}
	var got queries.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "c1" || got.AgentID != "a1" {
		t.Fatalf("body = %+v", got)
	}
}

func TestConversationGetNotFound(t *testing.T) {
	fake := &conversationServiceFake{err: services.NotFound("conversation")}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if fake.getID != "missing" {
		t.Fatalf("service id = %q, want missing", fake.getID)
	}
}

func TestConversationList(t *testing.T) {
	fake := &conversationServiceFake{convs: []queries.Conversation{conversationFixture()}}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations?agent_id=a1&limit=10&offset=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if fake.listAgent != "a1" {
		t.Fatalf("agent filter = %q, want a1", fake.listAgent)
	}
	if fake.listPage != (services.Page{Limit: 10, Offset: 5}) {
		t.Fatalf("page = %+v", fake.listPage)
	}
	var got struct {
		Data   []queries.Conversation `json:"data"`
		Limit  int                    `json:"limit"`
		Offset int                    `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "c1" || got.Limit != 10 || got.Offset != 5 {
		t.Fatalf("body = %+v", got)
	}
}

func TestConversationListDefaults(t *testing.T) {
	fake := &conversationServiceFake{}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if fake.listAgent != "" {
		t.Fatalf("agent filter = %q, want empty", fake.listAgent)
	}
	if fake.listPage != (services.Page{Limit: services.DefaultLimit, Offset: 0}) {
		t.Fatalf("page = %+v, want defaults", fake.listPage)
	}
}

func TestConversationListEmptyRendersArray(t *testing.T) {
	fake := &conversationServiceFake{convs: nil}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations?agent_id=none", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("body = %s, want data as []", rec.Body.String())
	}
}

func TestConversationListBadPagination(t *testing.T) {
	for _, target := range []string{
		"/v1/conversations?offset=-1",
		"/v1/conversations?limit=abc",
		"/v1/conversations?agent_id=a1&limit=-5",
	} {
		fake := &conversationServiceFake{}
		rec := conversationServe(t, fake, http.MethodGet, target, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", target, rec.Code)
		}
		if e := conversationErrorBody(t, rec); e.Code != httpx.CodeBadRequest {
			t.Fatalf("%s: code = %q, want bad_request", target, e.Code)
		}
		if fake.calls != 0 {
			t.Fatalf("%s: service called %d times", target, fake.calls)
		}
	}
}

func TestConversationDelete(t *testing.T) {
	fake := &conversationServiceFake{}
	rec := conversationServe(t, fake, http.MethodDelete, "/v1/conversations/c1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 body = %q, want empty", rec.Body.String())
	}
	if fake.deleteID != "c1" {
		t.Fatalf("service id = %q, want c1", fake.deleteID)
	}
}

func TestConversationDeleteNotFound(t *testing.T) {
	fake := &conversationServiceFake{err: services.NotFound("conversation")}
	rec := conversationServe(t, fake, http.MethodDelete, "/v1/conversations/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if e := conversationErrorBody(t, rec); e.Code != httpx.CodeNotFound {
		t.Fatalf("code = %q, want not_found", e.Code)
	}
}

func TestConversationListMessages(t *testing.T) {
	fake := &conversationServiceFake{messages: []queries.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
	}}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/c1/messages?limit=20&offset=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if fake.msgID != "c1" {
		t.Fatalf("service id = %q, want c1", fake.msgID)
	}
	if fake.msgPage != (services.Page{Limit: 20, Offset: 2}) {
		t.Fatalf("page = %+v", fake.msgPage)
	}
	var got struct {
		Data   []queries.Message `json:"data"`
		Limit  int               `json:"limit"`
		Offset int               `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "m1" || got.Data[0].Content != "hi" || got.Limit != 20 || got.Offset != 2 {
		t.Fatalf("body = %+v", got)
	}
}

func TestConversationListMessagesEmptyRendersArray(t *testing.T) {
	fake := &conversationServiceFake{}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/c1/messages", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("body = %s, want data as []", rec.Body.String())
	}
}

func TestConversationListMessagesNotFound(t *testing.T) {
	fake := &conversationServiceFake{err: services.NotFound("conversation")}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/missing/messages", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if fake.msgID != "missing" {
		t.Fatalf("service id = %q, want missing", fake.msgID)
	}
}

func TestConversationListMessagesBadPagination(t *testing.T) {
	fake := &conversationServiceFake{}
	rec := conversationServe(t, fake, http.MethodGet, "/v1/conversations/c1/messages?offset=-1", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if e := conversationErrorBody(t, rec); e.Code != httpx.CodeBadRequest {
		t.Fatalf("code = %q, want bad_request", e.Code)
	}
	if fake.calls != 0 {
		t.Fatalf("service called %d times", fake.calls)
	}
}

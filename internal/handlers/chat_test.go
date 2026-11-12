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

// chatServiceFake records what the handlers passed in and returns canned results.
type chatServiceFake struct {
	calls int

	sendConvID string
	sendIn     services.SendMessageInput
	sendOut    *services.ChatResult
	sendErr    error

	streamConvID string
	streamIn     services.SendMessageInput
	streamEvents []services.ChatEvent // replayed through emit, in order
	streamOut    *services.ChatResult
	streamErr    error

	runsConvID string
	runsPage   services.Page
	runsOut    []queries.Run
	runsErr    error

	getRunID  string
	getRunOut *services.RunDetail
	getRunErr error

	toolsOut []services.ToolInfo
}

var _ services.ChatService = (*chatServiceFake)(nil)

func (f *chatServiceFake) SendMessage(_ context.Context, conversationID string, in services.SendMessageInput) (*services.ChatResult, error) {
	f.calls++
	f.sendConvID = conversationID
	f.sendIn = in
	return f.sendOut, f.sendErr
}

func (f *chatServiceFake) StreamMessage(_ context.Context, conversationID string, in services.SendMessageInput, emit func(services.ChatEvent)) (*services.ChatResult, error) {
	f.calls++
	f.streamConvID = conversationID
	f.streamIn = in
	for _, ev := range f.streamEvents {
		emit(ev)
	}
	return f.streamOut, f.streamErr
}

func (f *chatServiceFake) GetRun(_ context.Context, id string) (*services.RunDetail, error) {
	f.calls++
	f.getRunID = id
	return f.getRunOut, f.getRunErr
}

func (f *chatServiceFake) ListRuns(_ context.Context, conversationID string, p services.Page) ([]queries.Run, error) {
	f.calls++
	f.runsConvID = conversationID
	f.runsPage = p
	return f.runsOut, f.runsErr
}

func (f *chatServiceFake) ListTools() []services.ToolInfo {
	f.calls++
	return f.toolsOut
}

var chatFixedTime = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

func chatSampleRun(id string) queries.Run {
	finished := chatFixedTime.Add(time.Second)
	return queries.Run{
		ID:             id,
		ConversationID: "conv_1",
		Status:         "completed",
		StopReason:     "end_turn",
		Error:          "",
		Model:          "claude-test",
		InputTokens:    12,
		OutputTokens:   34,
		Iterations:     2,
		StartedAt:      chatFixedTime,
		FinishedAt:     &finished,
	}
}

func chatSampleMessage(id, role, content string) queries.Message {
	return queries.Message{
		ID:             id,
		ConversationID: "conv_1",
		Role:           role,
		Content:        content,
		CreatedAt:      chatFixedTime,
	}
}

func chatSampleToolCall() queries.ToolCall {
	return queries.ToolCall{
		ID:         "tc_1",
		RunID:      "run_1",
		Name:       "echo",
		Input:      json.RawMessage(`{"text":"hi"}`),
		Output:     "hi",
		IsError:    false,
		DurationMS: 7,
		CreatedAt:  chatFixedTime,
	}
}

// chatServer mounts the handlers exactly as the server does.
func chatServer(t *testing.T, fake *chatServiceFake) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	New(Deps{Chat: fake}).Register(mux)
	return httpx.NewRouter(mux)
}

func chatDo(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
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

func chatErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, rec.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

func chatAssertJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// chatErrorCases are the service errors every chat route must map the same way.
var chatErrorCases = []struct {
	name       string
	err        error
	wantStatus int
	wantCode   string
	wantMsg    string
}{
	{"validation", services.Invalid("content is required"), http.StatusBadRequest, "validation_failed", "content is required"},
	{"not found", services.NotFound("conversation"), http.StatusNotFound, "not_found", "conversation not found"},
	{"upstream", services.Upstream(errors.New("anthropic: secret key rejected")), http.StatusBadGateway, "upstream_error", "the model provider request failed"},
	{"internal hides wrapped error", services.Internal(errors.New("pq: secret connection string")), http.StatusInternalServerError, "internal_error", "internal error"},
	{"unclassified error is internal", errors.New("boom secret"), http.StatusInternalServerError, "internal_error", "internal error"},
	{"deadline is timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout", "the request timed out"},
}

func chatAssertError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode, wantMsg string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, wantStatus, rec.Body.String())
	}
	code, msg := chatErrorBody(t, rec)
	// An empty wantMsg means the decoder's own wording is not pinned down.
	if code != wantCode || (wantMsg != "" && msg != wantMsg) {
		t.Errorf("error = %s/%s, want %s/%s", code, msg, wantCode, wantMsg)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("internal detail leaked: %s", rec.Body.String())
	}
}

// ---- POST /v1/conversations/{id}/messages ----------------------------------

func TestChatSendMessage(t *testing.T) {
	result := &services.ChatResult{
		UserMessage:      chatSampleMessage("msg_u", "user", "hello"),
		AssistantMessage: chatSampleMessage("msg_a", "assistant", "hi there"),
		Run:              chatSampleRun("run_1"),
		ToolCalls:        []queries.ToolCall{chatSampleToolCall()},
	}
	fake := &chatServiceFake{sendOut: result}
	rec := chatDo(t, chatServer(t, fake), http.MethodPost, "/v1/conversations/conv_1/messages", `{"content":"hello"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	chatAssertJSON(t, rec)

	var got services.ChatResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a ChatResult: %v: %s", err, rec.Body.String())
	}
	if !reflect.DeepEqual(got.UserMessage, result.UserMessage) ||
		!reflect.DeepEqual(got.AssistantMessage, result.AssistantMessage) ||
		!reflect.DeepEqual(got.Run, result.Run) {
		t.Errorf("result = %+v, want %+v", got, *result)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "echo" {
		t.Errorf("tool_calls = %+v, want one echo call", got.ToolCalls)
	}
	for _, key := range []string{`"user_message"`, `"assistant_message"`, `"run"`, `"tool_calls"`, `"input_tokens"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("body lacks key %s: %s", key, rec.Body.String())
		}
	}

	if fake.sendConvID != "conv_1" {
		t.Errorf("conversation id = %q, want conv_1", fake.sendConvID)
	}
	if fake.sendIn != (services.SendMessageInput{Content: "hello"}) {
		t.Errorf("service input = %+v, want content hello", fake.sendIn)
	}
	if fake.calls != 1 {
		t.Errorf("service calls = %d, want 1", fake.calls)
	}
}

func TestChatSendMessageServiceErrors(t *testing.T) {
	for _, tt := range chatErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{sendErr: tt.err}
			rec := chatDo(t, chatServer(t, fake), http.MethodPost, "/v1/conversations/conv_1/messages", `{"content":"hello"}`)
			chatAssertError(t, rec, tt.wantStatus, tt.wantCode, tt.wantMsg)
			if fake.calls != 1 {
				t.Errorf("service calls = %d, want 1", fake.calls)
			}
		})
	}
}

func TestChatSendMessageBadBodyNeverReachesService(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{"content":`},
		{"unknown field", `{"content":"x","model":"y"}`},
		{"wrong type", `{"content":42}`},
		{"empty body", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{}
			rec := chatDo(t, chatServer(t, fake), http.MethodPost, "/v1/conversations/conv_1/messages", tt.body)
			chatAssertError(t, rec, http.StatusBadRequest, "bad_request", "")
			if fake.calls != 0 {
				t.Errorf("service calls = %d, want 0", fake.calls)
			}
		})
	}
}

// ---- GET /v1/conversations/{id}/runs ---------------------------------------

func TestChatListRuns(t *testing.T) {
	fake := &chatServiceFake{runsOut: []queries.Run{chatSampleRun("run_1"), chatSampleRun("run_2")}}
	rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/conversations/conv_1/runs?limit=10&offset=5", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	chatAssertJSON(t, rec)

	var got struct {
		Data   []queries.Run `json:"data"`
		Limit  int           `json:"limit"`
		Offset int           `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a run list: %v: %s", err, rec.Body.String())
	}
	if len(got.Data) != 2 || got.Data[0].ID != "run_1" || got.Data[1].ID != "run_2" {
		t.Errorf("data = %+v", got.Data)
	}
	if got.Limit != 10 || got.Offset != 5 {
		t.Errorf("limit/offset = %d/%d, want 10/5", got.Limit, got.Offset)
	}
	if fake.runsConvID != "conv_1" {
		t.Errorf("conversation id = %q, want conv_1", fake.runsConvID)
	}
	if fake.runsPage != (services.Page{Limit: 10, Offset: 5}) {
		t.Errorf("page = %+v, want {10 5}", fake.runsPage)
	}
	if fake.calls != 1 {
		t.Errorf("service calls = %d, want 1", fake.calls)
	}
}

func TestChatListRunsDefaultsAndEmpty(t *testing.T) {
	fake := &chatServiceFake{runsOut: nil}
	rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/conversations/conv_1/runs", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("empty list must render data as []: %s", rec.Body.String())
	}
	if fake.runsPage != (services.Page{Limit: services.DefaultLimit, Offset: 0}) {
		t.Errorf("page = %+v, want defaults", fake.runsPage)
	}
}

func TestChatListRunsServiceErrors(t *testing.T) {
	for _, tt := range chatErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{runsErr: tt.err}
			rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/conversations/conv_1/runs", "")
			chatAssertError(t, rec, tt.wantStatus, tt.wantCode, tt.wantMsg)
		})
	}
}

func TestChatListRunsBadPagination(t *testing.T) {
	for _, target := range []string{
		"/v1/conversations/conv_1/runs?limit=abc",
		"/v1/conversations/conv_1/runs?offset=-1",
	} {
		t.Run(target, func(t *testing.T) {
			fake := &chatServiceFake{}
			rec := chatDo(t, chatServer(t, fake), http.MethodGet, target, "")
			chatAssertError(t, rec, http.StatusBadRequest, "bad_request", "")
			if fake.calls != 0 {
				t.Errorf("service calls = %d, want 0", fake.calls)
			}
		})
	}
}

// ---- GET /v1/runs/{id} -----------------------------------------------------

func TestChatGetRun(t *testing.T) {
	fake := &chatServiceFake{getRunOut: &services.RunDetail{
		Run:       chatSampleRun("run_1"),
		ToolCalls: []queries.ToolCall{chatSampleToolCall()},
	}}
	rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/runs/run_1", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	chatAssertJSON(t, rec)

	// The run's fields are flattened next to tool_calls, not nested under "run".
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not an object: %v: %s", err, rec.Body.String())
	}
	if _, nested := got["run"]; nested {
		t.Errorf("run must be flattened, got nested key: %s", rec.Body.String())
	}
	for _, key := range []string{"id", "conversation_id", "status", "stop_reason", "error", "model",
		"input_tokens", "output_tokens", "iterations", "started_at", "finished_at", "tool_calls"} {
		if _, ok := got[key]; !ok {
			t.Errorf("body lacks key %q: %s", key, rec.Body.String())
		}
	}
	var id string
	_ = json.Unmarshal(got["id"], &id)
	if id != "run_1" {
		t.Errorf("id = %q, want run_1", id)
	}
	var calls []queries.ToolCall
	if err := json.Unmarshal(got["tool_calls"], &calls); err != nil {
		t.Fatalf("tool_calls: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "echo" || string(calls[0].Input) != `{"text":"hi"}` {
		t.Errorf("tool_calls = %+v", calls)
	}

	if fake.getRunID != "run_1" {
		t.Errorf("run id = %q, want run_1", fake.getRunID)
	}
	if fake.calls != 1 {
		t.Errorf("service calls = %d, want 1", fake.calls)
	}
}

func TestChatGetRunServiceErrors(t *testing.T) {
	for _, tt := range chatErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{getRunErr: tt.err}
			rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/runs/run_1", "")
			chatAssertError(t, rec, tt.wantStatus, tt.wantCode, tt.wantMsg)
		})
	}
}

// ---- GET /v1/tools ---------------------------------------------------------

func TestChatListTools(t *testing.T) {
	fake := &chatServiceFake{toolsOut: []services.ToolInfo{{
		Name:        "echo",
		Description: "Echoes its input.",
		InputSchema: map[string]any{"type": "object"},
	}}}
	rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/tools", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	chatAssertJSON(t, rec)

	var got struct {
		Data []services.ToolInfo `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a tool list: %v: %s", err, rec.Body.String())
	}
	if len(got.Data) != 1 || got.Data[0].Name != "echo" || got.Data[0].InputSchema["type"] != "object" {
		t.Errorf("data = %+v", got.Data)
	}
	if !strings.Contains(rec.Body.String(), `"input_schema"`) {
		t.Errorf("body lacks input_schema: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"limit"`) || strings.Contains(rec.Body.String(), `"offset"`) {
		t.Errorf("tools must not carry pagination fields: %s", rec.Body.String())
	}
	if fake.calls != 1 {
		t.Errorf("service calls = %d, want 1", fake.calls)
	}
}

func TestChatListToolsEmptyRendersArray(t *testing.T) {
	fake := &chatServiceFake{toolsOut: nil}
	rec := chatDo(t, chatServer(t, fake), http.MethodGet, "/v1/tools", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"data":[]}` {
		t.Errorf("body = %s, want {\"data\":[]}", got)
	}
}

// ---- POST /v1/conversations/{id}/messages/stream ---------------------------

const chatStreamTarget = "/v1/conversations/conv_1/messages/stream"

type sseEvent struct {
	Event string
	Data  string
}

// parseSSE splits a text/event-stream body into its (event, data) pairs.
func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if block == "" {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			default:
				t.Fatalf("unexpected SSE line %q in %q", line, body)
			}
		}
		out = append(out, ev)
	}
	return out
}

func chatEventTypes(evs []sseEvent) []string {
	types := make([]string, 0, len(evs))
	for _, ev := range evs {
		types = append(types, ev.Event)
	}
	return types
}

func chatAssertSSE(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestChatStreamMessage(t *testing.T) {
	result := &services.ChatResult{
		UserMessage:      chatSampleMessage("msg_u", "user", "hello"),
		AssistantMessage: chatSampleMessage("msg_a", "assistant", "hi there"),
		Run:              chatSampleRun("run_1"),
		ToolCalls:        []queries.ToolCall{chatSampleToolCall()},
	}
	fake := &chatServiceFake{
		streamEvents: []services.ChatEvent{
			{Type: services.ChatEventStart, Data: services.ChatStartData{Run: result.Run, UserMessage: result.UserMessage}},
			{Type: services.ChatEventDelta, Data: services.ChatTextData{Text: "hi"}},
			{Type: services.ChatEventToolCall, Data: services.ChatToolData{Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}},
			{Type: services.ChatEventToolResult, Data: services.ChatToolData{Name: "echo", Input: json.RawMessage(`{"text":"hi"}`), Output: "hi", DurationMS: 7}},
			{Type: services.ChatEventDelta, Data: services.ChatTextData{Text: " there"}},
		},
		streamOut: result,
	}
	rec := chatDo(t, chatServer(t, fake), http.MethodPost, chatStreamTarget, `{"content":"hello"}`)
	chatAssertSSE(t, rec)

	evs := parseSSE(t, rec.Body.String())
	wantTypes := []string{"start", "delta", "tool_call", "tool_result", "delta", "done"}
	if got := chatEventTypes(evs); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event order = %v, want %v; body %s", got, wantTypes, rec.Body.String())
	}

	var start services.ChatStartData
	if err := json.Unmarshal([]byte(evs[0].Data), &start); err != nil {
		t.Fatalf("start data: %v", err)
	}
	if start.Run.ID != "run_1" || start.UserMessage.ID != "msg_u" {
		t.Errorf("start = %+v", start)
	}
	if evs[1].Data != `{"text":"hi"}` {
		t.Errorf("first delta data = %s", evs[1].Data)
	}
	var tool services.ChatToolData
	if err := json.Unmarshal([]byte(evs[3].Data), &tool); err != nil {
		t.Fatalf("tool_result data: %v", err)
	}
	if tool.Name != "echo" || tool.Output != "hi" || tool.DurationMS != 7 {
		t.Errorf("tool_result = %+v", tool)
	}

	var got services.ChatResult
	if err := json.Unmarshal([]byte(evs[5].Data), &got); err != nil {
		t.Fatalf("done data is not a ChatResult: %v: %s", err, evs[5].Data)
	}
	if !reflect.DeepEqual(got.UserMessage, result.UserMessage) ||
		!reflect.DeepEqual(got.AssistantMessage, result.AssistantMessage) ||
		!reflect.DeepEqual(got.Run, result.Run) ||
		len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "echo" {
		t.Errorf("done = %+v, want %+v", got, *result)
	}

	if fake.streamConvID != "conv_1" {
		t.Errorf("conversation id = %q, want conv_1", fake.streamConvID)
	}
	if fake.streamIn != (services.SendMessageInput{Content: "hello"}) {
		t.Errorf("service input = %+v, want content hello", fake.streamIn)
	}
	if fake.calls != 1 {
		t.Errorf("service calls = %d, want 1", fake.calls)
	}
}

func TestChatStreamMessageZeroEventsStillDone(t *testing.T) {
	result := &services.ChatResult{Run: chatSampleRun("run_1")}
	fake := &chatServiceFake{streamOut: result}
	rec := chatDo(t, chatServer(t, fake), http.MethodPost, chatStreamTarget, `{"content":"hello"}`)
	chatAssertSSE(t, rec)

	evs := parseSSE(t, rec.Body.String())
	if got := chatEventTypes(evs); !reflect.DeepEqual(got, []string{"done"}) {
		t.Fatalf("events = %v, want [done]; body %s", got, rec.Body.String())
	}
	var got services.ChatResult
	if err := json.Unmarshal([]byte(evs[0].Data), &got); err != nil || got.Run.ID != "run_1" {
		t.Errorf("done data = %s (%v)", evs[0].Data, err)
	}
}

func TestChatStreamMessageErrorBeforeEventIsJSON(t *testing.T) {
	for _, tt := range chatErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{streamErr: tt.err}
			rec := chatDo(t, chatServer(t, fake), http.MethodPost, chatStreamTarget, `{"content":"hello"}`)
			chatAssertError(t, rec, tt.wantStatus, tt.wantCode, tt.wantMsg)
			chatAssertJSON(t, rec)
			if fake.calls != 1 {
				t.Errorf("service calls = %d, want 1", fake.calls)
			}
		})
	}
}

func TestChatStreamMessageErrorAfterEventsIsErrorEvent(t *testing.T) {
	for _, tt := range chatErrorCases {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{
				streamEvents: []services.ChatEvent{
					{Type: services.ChatEventDelta, Data: services.ChatTextData{Text: "partial"}},
				},
				streamErr: tt.err,
			}
			rec := chatDo(t, chatServer(t, fake), http.MethodPost, chatStreamTarget, `{"content":"hello"}`)
			chatAssertSSE(t, rec)

			evs := parseSSE(t, rec.Body.String())
			if got := chatEventTypes(evs); !reflect.DeepEqual(got, []string{"delta", "error"}) {
				t.Fatalf("events = %v, want [delta error]; body %s", got, rec.Body.String())
			}
			var body httpx.ErrorBody
			if err := json.Unmarshal([]byte(evs[1].Data), &body); err != nil {
				t.Fatalf("error data is not the error body: %v: %s", err, evs[1].Data)
			}
			if body.Error.Code != tt.wantCode || (tt.wantMsg != "" && body.Error.Message != tt.wantMsg) {
				t.Errorf("error = %s/%s, want %s/%s", body.Error.Code, body.Error.Message, tt.wantCode, tt.wantMsg)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("internal detail leaked: %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `"done"`) || strings.Contains(rec.Body.String(), "event: done") {
				t.Errorf("done must not follow an error: %s", rec.Body.String())
			}
		})
	}
}

func TestChatStreamMessageBadBodyNeverReachesService(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{"content":`},
		{"unknown field", `{"content":"x","model":"y"}`},
		{"wrong type", `{"content":42}`},
		{"empty body", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &chatServiceFake{}
			rec := chatDo(t, chatServer(t, fake), http.MethodPost, chatStreamTarget, tt.body)
			chatAssertError(t, rec, http.StatusBadRequest, "bad_request", "")
			chatAssertJSON(t, rec)
			if fake.calls != 0 {
				t.Errorf("service calls = %d, want 0", fake.calls)
			}
		})
	}
}

func TestChatSendMessageStillWorksBesideStream(t *testing.T) {
	fake := &chatServiceFake{
		sendOut:      &services.ChatResult{Run: chatSampleRun("run_1")},
		streamEvents: []services.ChatEvent{{Type: services.ChatEventDelta, Data: services.ChatTextData{Text: "x"}}},
	}
	rec := chatDo(t, chatServer(t, fake), http.MethodPost, "/v1/conversations/conv_1/messages", `{"content":"hello"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	chatAssertJSON(t, rec)
	if fake.sendConvID != "conv_1" || fake.sendIn != (services.SendMessageInput{Content: "hello"}) {
		t.Errorf("send got conv %q input %+v", fake.sendConvID, fake.sendIn)
	}
	if fake.streamConvID != "" {
		t.Errorf("non-streaming route reached StreamMessage")
	}
}

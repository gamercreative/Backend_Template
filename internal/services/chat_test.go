package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"alphahack/internal/ai"
	"alphahack/internal/queries"
)

// ---- fakes -----------------------------------------------------------------

// chatFakeStore is an in-memory ChatStore. It embeds a nil queries.Querier so
// it can be passed to InTx callbacks; only the methods used are overridden.
type chatFakeStore struct {
	queries.Querier

	agents   map[string]queries.Agent
	convs    map[string]queries.Conversation
	messages []queries.Message
	runs     map[string]queries.Run
	runOrder []string
	calls    []queries.ToolCall
	touched  int
	seq      int

	failCreateToolCall error
	failFinishRun      error // only for Status == completed
	failListRecent     error
}

func chatNewFakeStore() *chatFakeStore {
	return &chatFakeStore{
		agents: map[string]queries.Agent{
			"a1": {ID: "a1", Name: "bot", SystemPrompt: "be nice", Model: "agent-model"},
			"a2": {ID: "a2", Name: "plain", SystemPrompt: "", Model: ""},
		},
		convs: map[string]queries.Conversation{
			"c1": {ID: "c1", AgentID: "a1"},
			"c2": {ID: "c2", AgentID: "a2"},
		},
		runs: map[string]queries.Run{},
	}
}

func (f *chatFakeStore) id(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s%d", prefix, f.seq)
}

func (f *chatFakeStore) InTx(ctx context.Context, fn func(q queries.Querier) error) error {
	return fn(f)
}

func (f *chatFakeStore) GetAgent(_ context.Context, id string) (queries.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return queries.Agent{}, queries.ErrNotFound
	}
	return a, nil
}

func (f *chatFakeStore) GetConversation(_ context.Context, id string) (queries.Conversation, error) {
	c, ok := f.convs[id]
	if !ok {
		return queries.Conversation{}, queries.ErrNotFound
	}
	return c, nil
}

func (f *chatFakeStore) TouchConversation(_ context.Context, id string) error {
	f.touched++
	return nil
}

func (f *chatFakeStore) CreateMessage(_ context.Context, p queries.CreateMessageParams) (queries.Message, error) {
	m := queries.Message{ID: f.id("m"), ConversationID: p.ConversationID, RunID: p.RunID, Role: p.Role, Content: p.Content}
	f.messages = append(f.messages, m)
	return m, nil
}

func (f *chatFakeStore) ListRecentMessages(_ context.Context, conversationID string, limit int) ([]queries.Message, error) {
	if f.failListRecent != nil {
		return nil, f.failListRecent
	}
	var all []queries.Message
	for _, m := range f.messages {
		if m.ConversationID == conversationID {
			all = append(all, m)
		}
	}
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (f *chatFakeStore) CreateRun(_ context.Context, p queries.CreateRunParams) (queries.Run, error) {
	r := queries.Run{ID: f.id("r"), ConversationID: p.ConversationID, Status: queries.RunStatusRunning, Model: p.Model}
	f.runs[r.ID] = r
	f.runOrder = append(f.runOrder, r.ID)
	return r, nil
}

func (f *chatFakeStore) FinishRun(_ context.Context, p queries.FinishRunParams) (queries.Run, error) {
	if f.failFinishRun != nil && p.Status == queries.RunStatusCompleted {
		return queries.Run{}, f.failFinishRun
	}
	r, ok := f.runs[p.ID]
	if !ok {
		return queries.Run{}, queries.ErrNotFound
	}
	now := time.Now()
	r.Status, r.StopReason, r.Error, r.Model = p.Status, p.StopReason, p.Error, p.Model
	r.InputTokens, r.OutputTokens, r.Iterations, r.FinishedAt = p.InputTokens, p.OutputTokens, p.Iterations, &now
	f.runs[p.ID] = r
	return r, nil
}

func (f *chatFakeStore) GetRun(_ context.Context, id string) (queries.Run, error) {
	r, ok := f.runs[id]
	if !ok {
		return queries.Run{}, queries.ErrNotFound
	}
	return r, nil
}

func (f *chatFakeStore) ListRuns(_ context.Context, conversationID string, limit, offset int) ([]queries.Run, error) {
	out := []queries.Run{}
	for _, id := range f.runOrder {
		if r := f.runs[id]; r.ConversationID == conversationID {
			out = append(out, r)
		}
	}
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *chatFakeStore) CreateToolCall(_ context.Context, p queries.CreateToolCallParams) (queries.ToolCall, error) {
	if f.failCreateToolCall != nil {
		return queries.ToolCall{}, f.failCreateToolCall
	}
	tc := queries.ToolCall{ID: f.id("t"), RunID: p.RunID, Name: p.Name, Input: p.Input, Output: p.Output, IsError: p.IsError, DurationMS: p.DurationMS}
	f.calls = append(f.calls, tc)
	return tc, nil
}

func (f *chatFakeStore) ListToolCalls(_ context.Context, runID string) ([]queries.ToolCall, error) {
	out := []queries.ToolCall{}
	for _, c := range f.calls {
		if c.RunID == runID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *chatFakeStore) messagesByRole(role string) []queries.Message {
	var out []queries.Message
	for _, m := range f.messages {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

// chatFakeClient records requests and returns a canned result/error. When
// req.OnEvent is set it first replays events through it, in order.
type chatFakeClient struct {
	reqs   []ai.Request
	result *ai.Result
	err    error
	events []ai.Event
}

func (c *chatFakeClient) Run(ctx context.Context, req ai.Request) (*ai.Result, error) {
	c.reqs = append(c.reqs, req)
	if req.OnEvent != nil {
		for _, e := range c.events {
			req.OnEvent(e)
		}
	}
	return c.result, c.err
}

func chatOKResult() *ai.Result {
	return &ai.Result{
		Text: "hello there", StopReason: ai.StopEndTurn, Model: "served-model",
		InputTokens: 10, OutputTokens: 5, Iterations: 2,
		ToolCalls: []ai.ToolCall{
			{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"UTC"}`), Output: "2026-01-01T00:00:00Z", Duration: 1500 * time.Millisecond},
			{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"Nope"}`), Output: "unknown timezone", IsError: true},
		},
	}
}

func chatSetup(res *ai.Result, err error, opts ChatOptions) (*chatFakeStore, *chatFakeClient, ChatService) {
	st := chatNewFakeStore()
	cl := &chatFakeClient{result: res, err: err}
	return st, cl, NewChatService(st, cl, DefaultTools(), opts)
}

func chatErrCode(err error) string {
	var se *Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// ---- SendMessage -----------------------------------------------------------

func TestChatSendMessageHappyPath(t *testing.T) {
	st, cl, svc := chatSetup(chatOKResult(), nil, ChatOptions{DefaultModel: "default-model", MaxIterations: 7})

	out, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "  what time is it?  "})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if out.UserMessage.Content != "what time is it?" || out.UserMessage.Role != queries.RoleUser {
		t.Errorf("user message = %+v", out.UserMessage)
	}
	if out.AssistantMessage.Content != "hello there" || out.AssistantMessage.Role != queries.RoleAssistant {
		t.Errorf("assistant message = %+v", out.AssistantMessage)
	}
	if out.UserMessage.RunID == nil || *out.UserMessage.RunID != out.Run.ID ||
		out.AssistantMessage.RunID == nil || *out.AssistantMessage.RunID != out.Run.ID {
		t.Errorf("messages not linked to run %s", out.Run.ID)
	}
	r := out.Run
	if r.Status != queries.RunStatusCompleted || r.StopReason != ai.StopEndTurn || r.Model != "served-model" ||
		r.InputTokens != 10 || r.OutputTokens != 5 || r.Iterations != 2 || r.FinishedAt == nil {
		t.Errorf("run = %+v", r)
	}
	if len(out.ToolCalls) != 2 || len(st.calls) != 2 {
		t.Fatalf("tool calls: result %d, stored %d", len(out.ToolCalls), len(st.calls))
	}
	if out.ToolCalls[0].DurationMS != 1500 || out.ToolCalls[0].RunID != r.ID || !out.ToolCalls[1].IsError {
		t.Errorf("tool calls = %+v", out.ToolCalls)
	}
	if st.touched != 1 {
		t.Errorf("touched = %d", st.touched)
	}

	if len(cl.reqs) != 1 {
		t.Fatalf("client calls = %d", len(cl.reqs))
	}
	req := cl.reqs[0]
	if req.Model != "agent-model" || req.System != "be nice" || req.MaxIterations != 7 || len(req.Tools) != 1 {
		t.Errorf("request = %+v", req)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != ai.RoleUser || req.Messages[0].Content != "what time is it?" {
		t.Errorf("history = %+v", req.Messages)
	}
}

func TestChatSendMessageNoToolCallsNonNil(t *testing.T) {
	_, _, svc := chatSetup(&ai.Result{Text: "hi", StopReason: ai.StopEndTurn}, nil, ChatOptions{})
	out, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ToolCalls == nil || len(out.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %#v, want non-nil empty", out.ToolCalls)
	}
	if out.Run.Model != "agent-model" {
		t.Errorf("run model = %q, want request model when result model is empty", out.Run.Model)
	}
}

func TestChatSendMessageValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		ok      bool
	}{
		{"empty", "", false},
		{"whitespace", " \n\t ", false},
		{"too long", strings.Repeat("é", MaxContentLen+1), false},
		{"max runes", strings.Repeat("é", MaxContentLen), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, cl, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{})
			_, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: tt.content})
			if tt.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if chatErrCode(err) != CodeInvalid {
				t.Fatalf("err = %v, want validation_failed", err)
			}
			if len(st.messages) != 0 || len(st.runs) != 0 || len(cl.reqs) != 0 {
				t.Errorf("side effects on invalid input")
			}
		})
	}
}

func TestChatSendMessageUnknownConversation(t *testing.T) {
	st, cl, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{})
	_, err := svc.SendMessage(context.Background(), "nope", SendMessageInput{Content: "hi"})
	if chatErrCode(err) != CodeNotFound || !strings.Contains(err.Error(), "conversation") {
		t.Fatalf("err = %v", err)
	}
	if len(st.runs) != 0 || len(cl.reqs) != 0 {
		t.Error("side effects for unknown conversation")
	}
}

func TestChatSendMessageModelFallback(t *testing.T) {
	_, cl, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{DefaultModel: "default-model"})
	out, err := svc.SendMessage(context.Background(), "c2", SendMessageInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if cl.reqs[0].Model != "default-model" || out.Run.Model != "default-model" {
		t.Errorf("model = %q / run %q", cl.reqs[0].Model, out.Run.Model)
	}
}

func TestChatSendMessageHistoryTrimming(t *testing.T) {
	st, cl, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{HistoryLimit: 4})
	// Prior conversation: the oldest kept message (after the limit) is an
	// assistant message and there is an empty-content message.
	st.messages = []queries.Message{
		{ConversationID: "c1", Role: queries.RoleUser, Content: "u0"},
		{ConversationID: "c1", Role: queries.RoleAssistant, Content: "a0"},
		{ConversationID: "c1", Role: queries.RoleUser, Content: "u1"},
		{ConversationID: "c1", Role: queries.RoleAssistant, Content: "a1"},
		{ConversationID: "c1", Role: queries.RoleUser, Content: "u2"},
		{ConversationID: "c1", Role: queries.RoleAssistant, Content: ""},
	}
	if _, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "u3"}); err != nil {
		t.Fatal(err)
	}
	got := cl.reqs[0].Messages
	// last 4: a1, u2, "", u3 -> drop leading assistant and empty -> u2, u3
	want := []ai.Message{{Role: ai.RoleUser, Content: "u2"}, {Role: ai.RoleUser, Content: "u3"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("history = %+v, want %+v", got, want)
	}
}

func TestChatSendMessageDefaultHistoryLimit(t *testing.T) {
	st, cl, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{})
	for i := 0; i < 50; i++ {
		role := queries.RoleUser
		if i%2 == 1 {
			role = queries.RoleAssistant
		}
		st.messages = append(st.messages, queries.Message{ConversationID: "c1", Role: role, Content: fmt.Sprint(i)})
	}
	if _, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "last"}); err != nil {
		t.Fatal(err)
	}
	got := cl.reqs[0].Messages
	if len(got) > 40 || got[0].Role != ai.RoleUser || got[len(got)-1].Content != "last" {
		t.Errorf("history len %d first %+v last %+v", len(got), got[0], got[len(got)-1])
	}
}

func TestChatSendMessageEmptyTextPlaceholder(t *testing.T) {
	_, _, svc := chatSetup(&ai.Result{Text: "", StopReason: ai.StopMaxIterations}, nil, ChatOptions{})
	out, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "[no text response; stop reason: max_iterations]"; out.AssistantMessage.Content != want {
		t.Errorf("content = %q, want %q", out.AssistantMessage.Content, want)
	}
	if out.Run.Status != queries.RunStatusCompleted {
		t.Errorf("status = %q", out.Run.Status)
	}
}

func TestChatSendMessageAIFailurePartial(t *testing.T) {
	partial := &ai.Result{
		StopReason: "", Model: "served", InputTokens: 3, Iterations: 1,
		ToolCalls: []ai.ToolCall{{Name: "get_current_time", Input: json.RawMessage(`{}`), Output: "x"}},
	}
	longErr := errors.New(strings.Repeat("x", 3000))
	st, _, svc := chatSetup(partial, longErr, ChatOptions{})

	_, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if chatErrCode(err) != CodeUpstream || !errors.Is(err, longErr) {
		t.Fatalf("err = %v, want upstream wrapping cause", err)
	}
	if n := len(st.messagesByRole(queries.RoleUser)); n != 1 {
		t.Errorf("user messages = %d, want 1", n)
	}
	if n := len(st.messagesByRole(queries.RoleAssistant)); n != 0 {
		t.Errorf("assistant messages = %d, want 0", n)
	}
	if len(st.calls) != 1 {
		t.Errorf("stored tool calls = %d, want 1", len(st.calls))
	}
	r := st.runs[st.runOrder[0]]
	if r.Status != queries.RunStatusFailed || len(r.Error) != 2000 || r.InputTokens != 3 || r.Model != "served" {
		t.Errorf("run = status %q, err len %d, tokens %d, model %q", r.Status, len(r.Error), r.InputTokens, r.Model)
	}
}

func TestChatSendMessageAIFailureNoPartial(t *testing.T) {
	st, _, svc := chatSetup(nil, errors.New("boom"), ChatOptions{})
	_, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if chatErrCode(err) != CodeUpstream {
		t.Fatalf("err = %v", err)
	}
	r := st.runs[st.runOrder[0]]
	if r.Status != queries.RunStatusFailed || r.Error != "boom" || r.Model != "agent-model" {
		t.Errorf("run = %+v", r)
	}
}

func TestChatSendMessageDeadlinePassthrough(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, fmt.Errorf("call: %w", context.Canceled)} {
		st, _, svc := chatSetup(nil, cause, ChatOptions{})
		_, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
		if err != cause {
			t.Errorf("err = %v, want %v unchanged", err, cause)
		}
		if r := st.runs[st.runOrder[0]]; r.Status != queries.RunStatusFailed {
			t.Errorf("run status = %q", r.Status)
		}
	}
}

func TestChatSendMessageCanceledContextStillRecordsFailure(t *testing.T) {
	st, _, svc := chatSetup(nil, context.Canceled, ChatOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The fake store ignores ctx, so the transaction runs; this checks that the
	// failure path does not bail out early on a dead context.
	_, err := svc.SendMessage(ctx, "c1", SendMessageInput{Content: "hi"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if r := st.runs[st.runOrder[0]]; r.Status != queries.RunStatusFailed {
		t.Errorf("run status = %q", r.Status)
	}
}

func TestChatSendMessagePersistenceFailure(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*chatFakeStore)
	}{
		{"tool call", func(s *chatFakeStore) { s.failCreateToolCall = errors.New("disk full") }},
		{"finish run", func(s *chatFakeStore) { s.failFinishRun = errors.New("disk full") }},
		{"history", func(s *chatFakeStore) { s.failListRecent = errors.New("disk full") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, svc := chatSetup(&ai.Result{Text: "ok", ToolCalls: []ai.ToolCall{{Name: "x"}}}, nil, ChatOptions{})
			tt.setup(st)
			_, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
			if chatErrCode(err) != CodeInternal {
				t.Fatalf("err = %v, want internal_error", err)
			}
		})
	}
}

// ---- GetRun / ListRuns / ListTools -----------------------------------------

func TestChatGetRun(t *testing.T) {
	_, _, svc := chatSetup(chatOKResult(), nil, ChatOptions{})
	out, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.GetRun(context.Background(), out.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != out.Run.ID || len(d.ToolCalls) != 2 {
		t.Errorf("detail = %+v", d)
	}

	if _, err := svc.GetRun(context.Background(), "missing"); chatErrCode(err) != CodeNotFound {
		t.Errorf("missing run err = %v", err)
	}
}

func TestChatListRuns(t *testing.T) {
	_, _, svc := chatSetup(&ai.Result{Text: "ok"}, nil, ChatOptions{})
	for i := 0; i < 3; i++ {
		if _, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := svc.ListRuns(context.Background(), "c1", Page{Limit: 2})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, err %v", len(runs), err)
	}
	runs, err = svc.ListRuns(context.Background(), "c2", Page{})
	if err != nil || runs == nil || len(runs) != 0 {
		t.Fatalf("empty conv runs = %#v, err %v", runs, err)
	}
	if _, err := svc.ListRuns(context.Background(), "nope", Page{}); chatErrCode(err) != CodeNotFound {
		t.Errorf("unknown conversation err = %v", err)
	}
}

func TestChatListTools(t *testing.T) {
	tools := append(DefaultTools(), ai.Tool{Name: "bare", Description: "no params"})
	svc := NewChatService(chatNewFakeStore(), &chatFakeClient{}, tools, ChatOptions{})
	infos := svc.ListTools()
	if len(infos) != 2 {
		t.Fatalf("tools = %d", len(infos))
	}
	gt := infos[0]
	if gt.Name != "get_current_time" || gt.Description == "" {
		t.Errorf("info = %+v", gt)
	}
	if gt.InputSchema["type"] != "object" {
		t.Errorf("schema type = %v", gt.InputSchema["type"])
	}
	props, ok := gt.InputSchema["properties"].(map[string]any)
	if !ok || props["timezone"] == nil {
		t.Errorf("properties = %#v", gt.InputSchema["properties"])
	}
	b, err := json.Marshal(infos[1].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"properties":{},"required":[],"type":"object"}` {
		t.Errorf("bare schema = %s", b)
	}
}

// ---- StreamMessage ---------------------------------------------------------

// chatRecorder collects emitted events.
type chatRecorder struct{ events []ChatEvent }

func (r *chatRecorder) emit(e ChatEvent) { r.events = append(r.events, e) }

func (r *chatRecorder) types() []string {
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

// chatScriptedEvents is a turn with thinking, text, one tool and more text,
// plus events the adapter must drop (empty text, nil tool, unknown type).
func chatScriptedEvents() []ai.Event {
	call := &ai.ToolCall{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"UTC"}`)}
	done := &ai.ToolCall{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"UTC"}`),
		Output: "2026-01-01T00:00:00Z", IsError: true, Duration: 1500 * time.Millisecond}
	return []ai.Event{
		{Type: ai.EventThinkingDelta, Text: "hmm"},
		{Type: ai.EventTextDelta, Text: "let me "},
		{Type: ai.EventTextDelta, Text: ""},
		{Type: ai.EventTextDelta, Text: "check"},
		{Type: ai.EventToolCall, Tool: call},
		{Type: ai.EventToolCall},
		{Type: ai.EventToolResult, Tool: done},
		{Type: ai.EventToolResult},
		{Type: "mystery", Text: "x"},
		{Type: ai.EventThinkingDelta, Text: ""},
		{Type: ai.EventTextDelta, Text: "hello "},
		{Type: ai.EventTextDelta, Text: "there"},
	}
}

func chatStreamSetup(res *ai.Result, err error) (*chatFakeStore, *chatFakeClient, ChatService) {
	st, cl, svc := chatSetup(res, err, ChatOptions{DefaultModel: "default-model", MaxIterations: 7})
	cl.events = chatScriptedEvents()
	return st, cl, svc
}

func TestChatStreamMessageEventOrderAndPayloads(t *testing.T) {
	st, cl, svc := chatStreamSetup(chatOKResult(), nil)
	var rec chatRecorder
	out, err := svc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: " what time? "}, rec.emit)
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	if len(cl.reqs) != 1 || cl.reqs[0].OnEvent == nil {
		t.Fatalf("OnEvent not set on request")
	}

	wantTypes := []string{
		ChatEventStart, ChatEventThinking, ChatEventDelta, ChatEventDelta,
		ChatEventToolCall, ChatEventToolResult, ChatEventDelta, ChatEventDelta,
	}
	if fmt.Sprint(rec.types()) != fmt.Sprint(wantTypes) {
		t.Fatalf("types = %v, want %v", rec.types(), wantTypes)
	}

	start, ok := rec.events[0].Data.(ChatStartData)
	if !ok {
		t.Fatalf("start data = %T", rec.events[0].Data)
	}
	if start.Run.ID != st.runOrder[0] || start.Run.ID != out.Run.ID || start.Run.Status != queries.RunStatusRunning {
		t.Errorf("start run = %+v, stored %q", start.Run, st.runOrder[0])
	}
	if start.UserMessage.ID != out.UserMessage.ID || start.UserMessage.Content != "what time?" ||
		start.UserMessage.RunID == nil || *start.UserMessage.RunID != start.Run.ID {
		t.Errorf("start user message = %+v", start.UserMessage)
	}

	texts := map[int]string{1: "hmm", 2: "let me ", 3: "check", 6: "hello ", 7: "there"}
	for i, want := range texts {
		d, ok := rec.events[i].Data.(ChatTextData)
		if !ok || d.Text != want {
			t.Errorf("event %d data = %#v, want text %q", i, rec.events[i].Data, want)
		}
	}

	tc, ok := rec.events[4].Data.(ChatToolData)
	want := ChatToolData{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"UTC"}`)}
	if !ok || fmt.Sprint(tc) != fmt.Sprint(want) {
		t.Errorf("tool_call = %#v, want %#v", rec.events[4].Data, want)
	}
	tr, ok := rec.events[5].Data.(ChatToolData)
	want = ChatToolData{Name: "get_current_time", Input: json.RawMessage(`{"timezone":"UTC"}`),
		Output: "2026-01-01T00:00:00Z", IsError: true, DurationMS: 1500}
	if !ok || fmt.Sprint(tr) != fmt.Sprint(want) {
		t.Errorf("tool_result = %#v, want %#v", rec.events[5].Data, want)
	}
	for i, e := range rec.events {
		if _, err := json.Marshal(e.Data); err != nil {
			t.Errorf("event %d not marshalable: %v", i, err)
		}
	}
}

func TestChatStreamMessageStartBeforeHistory(t *testing.T) {
	st, cl, svc := chatStreamSetup(nil, nil)
	st.failListRecent = errors.New("disk full")
	var rec chatRecorder
	_, err := svc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}, rec.emit)
	if chatErrCode(err) != CodeInternal {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(rec.types()) != fmt.Sprint([]string{ChatEventStart}) {
		t.Errorf("events = %v, want only start", rec.types())
	}
	if len(cl.reqs) != 0 {
		t.Errorf("client called")
	}
	if r := st.runs[st.runOrder[0]]; r.Status != queries.RunStatusFailed {
		t.Errorf("run status = %q", r.Status)
	}
}

func TestChatSendMessageDoesNotStream(t *testing.T) {
	_, cl, svc := chatStreamSetup(chatOKResult(), nil)
	if _, err := svc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if cl.reqs[0].OnEvent != nil {
		t.Error("SendMessage set OnEvent")
	}
}

func TestChatStreamMessageNilEmit(t *testing.T) {
	st, cl, svc := chatStreamSetup(chatOKResult(), nil)
	out, err := svc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cl.reqs[0].OnEvent != nil {
		t.Error("nil emit set OnEvent")
	}
	if out.Run.Status != queries.RunStatusCompleted || len(out.ToolCalls) != 2 || len(st.messages) != 2 {
		t.Errorf("result = %+v", out)
	}
}

func TestChatStreamMessageResultMatchesSendMessage(t *testing.T) {
	_, sendCl, sendSvc := chatStreamSetup(chatOKResult(), nil)
	want, err := sendSvc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	_, streamCl, streamSvc := chatStreamSetup(chatOKResult(), nil)
	var rec chatRecorder
	got, err := streamSvc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}, rec.emit)
	if err != nil {
		t.Fatal(err)
	}
	// FinishedAt is time.Now(); compare everything else.
	if want.Run.FinishedAt == nil || got.Run.FinishedAt == nil {
		t.Fatal("FinishedAt not set")
	}
	want.Run.FinishedAt, got.Run.FinishedAt = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stream result\n %+v\nsend result\n %+v", got, want)
	}
	// The request differs only by OnEvent.
	sr, tr := sendCl.reqs[0], streamCl.reqs[0]
	tr.OnEvent = nil
	if fmt.Sprintf("%+v", sr) != fmt.Sprintf("%+v", tr) {
		t.Errorf("requests differ:\n %+v\n %+v", sr, tr)
	}
}

func TestChatStreamMessageNoEventsBeforeLoad(t *testing.T) {
	tests := []struct {
		name string
		conv string
		text string
		code string
	}{
		{"empty content", "c1", "  ", CodeInvalid},
		{"too long", "c1", strings.Repeat("a", MaxContentLen+1), CodeInvalid},
		{"unknown conversation", "nope", "hi", CodeNotFound},
		{"unknown agent", "c-orphan", "hi", CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, cl, svc := chatStreamSetup(chatOKResult(), nil)
			st.convs["c-orphan"] = queries.Conversation{ID: "c-orphan", AgentID: "gone"}
			var rec chatRecorder
			out, err := svc.StreamMessage(context.Background(), tt.conv, SendMessageInput{Content: tt.text}, rec.emit)
			if out != nil || chatErrCode(err) != tt.code {
				t.Fatalf("out %v err %v, want code %s", out, err, tt.code)
			}
			if len(rec.events) != 0 {
				t.Errorf("events = %v, want none", rec.types())
			}
			if len(st.runs) != 0 || len(st.messages) != 0 || len(cl.reqs) != 0 {
				t.Error("side effects")
			}
		})
	}
}

func TestChatStreamMessageAIFailureAfterEvents(t *testing.T) {
	partial := &ai.Result{Model: "served", InputTokens: 3, Iterations: 1,
		ToolCalls: []ai.ToolCall{{Name: "get_current_time", Input: json.RawMessage(`{}`), Output: "x"}}}
	cause := errors.New("provider exploded")

	sendSt, _, sendSvc := chatStreamSetup(partial, cause)
	_, sendErr := sendSvc.SendMessage(context.Background(), "c1", SendMessageInput{Content: "hi"})

	st, _, svc := chatStreamSetup(partial, cause)
	var rec chatRecorder
	out, err := svc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}, rec.emit)
	if out != nil || chatErrCode(err) != CodeUpstream || !errors.Is(err, cause) {
		t.Fatalf("out %v err %v", out, err)
	}
	if err.Error() != sendErr.Error() || chatErrCode(err) != chatErrCode(sendErr) {
		t.Errorf("stream err %q, send err %q", err, sendErr)
	}
	if len(rec.events) != 8 || rec.events[len(rec.events)-1].Type != ChatEventDelta {
		t.Errorf("events = %v; errors must not be emitted", rec.types())
	}
	r := st.runs[st.runOrder[0]]
	sr := sendSt.runs[sendSt.runOrder[0]]
	if r.Status != queries.RunStatusFailed || r.Error != "provider exploded" || r.Model != "served" || r.InputTokens != 3 {
		t.Errorf("run = %+v", r)
	}
	if r.Status != sr.Status || r.Error != sr.Error || len(st.calls) != len(sendSt.calls) || len(st.calls) != 1 {
		t.Errorf("stream run %+v / send run %+v", r, sr)
	}
	if n := len(st.messagesByRole(queries.RoleAssistant)); n != 0 {
		t.Errorf("assistant messages = %d", n)
	}
}

func TestChatStreamMessageDeadlinePassthrough(t *testing.T) {
	st, _, svc := chatStreamSetup(nil, context.DeadlineExceeded)
	var rec chatRecorder
	_, err := svc.StreamMessage(context.Background(), "c1", SendMessageInput{Content: "hi"}, rec.emit)
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v", err)
	}
	if r := st.runs[st.runOrder[0]]; r.Status != queries.RunStatusFailed {
		t.Errorf("run status = %q", r.Status)
	}
}

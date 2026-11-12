package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"alphahack/internal/queries"
)

// conversationStoreFake is an in-memory ConversationStore.
type conversationStoreFake struct {
	agents        map[string]queries.Agent
	conversations map[string]queries.Conversation
	messages      map[string][]queries.Message

	getAgentErr         error
	createErr           error
	getConvErr          error
	listConvErr         error
	deleteErr           error
	listMessagesErr     error
	createdParams       []queries.CreateConversationParams
	lastListAgentID     string
	lastListLimit       int
	lastListOffset      int
	lastMsgLimit        int
	lastMsgOffset       int
	lastMsgConversation string
	getConvCalls        int
	nextID              int
}

func newConversationStoreFake() *conversationStoreFake {
	return &conversationStoreFake{
		agents:        map[string]queries.Agent{"agent-1": {ID: "agent-1", Name: "A"}},
		conversations: map[string]queries.Conversation{},
		messages:      map[string][]queries.Message{},
	}
}

func (f *conversationStoreFake) GetAgent(_ context.Context, id string) (queries.Agent, error) {
	if f.getAgentErr != nil {
		return queries.Agent{}, f.getAgentErr
	}
	a, ok := f.agents[id]
	if !ok {
		return queries.Agent{}, queries.ErrNotFound
	}
	return a, nil
}

func (f *conversationStoreFake) CreateConversation(_ context.Context, p queries.CreateConversationParams) (queries.Conversation, error) {
	if f.createErr != nil {
		return queries.Conversation{}, f.createErr
	}
	f.createdParams = append(f.createdParams, p)
	f.nextID++
	c := queries.Conversation{ID: "conv-" + string(rune('0'+f.nextID)), AgentID: p.AgentID, Title: p.Title}
	f.conversations[c.ID] = c
	return c, nil
}

func (f *conversationStoreFake) GetConversation(_ context.Context, id string) (queries.Conversation, error) {
	f.getConvCalls++
	if f.getConvErr != nil {
		return queries.Conversation{}, f.getConvErr
	}
	c, ok := f.conversations[id]
	if !ok {
		return queries.Conversation{}, queries.ErrNotFound
	}
	return c, nil
}

func (f *conversationStoreFake) ListConversations(_ context.Context, agentID string, limit, offset int) ([]queries.Conversation, error) {
	f.lastListAgentID, f.lastListLimit, f.lastListOffset = agentID, limit, offset
	if f.listConvErr != nil {
		return nil, f.listConvErr
	}
	out := []queries.Conversation{}
	for _, c := range f.conversations {
		if agentID == "" || c.AgentID == agentID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *conversationStoreFake) DeleteConversation(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.conversations[id]; !ok {
		return queries.ErrNotFound
	}
	delete(f.conversations, id)
	return nil
}

func (f *conversationStoreFake) ListMessages(_ context.Context, conversationID string, limit, offset int) ([]queries.Message, error) {
	f.lastMsgConversation, f.lastMsgLimit, f.lastMsgOffset = conversationID, limit, offset
	if f.listMessagesErr != nil {
		return nil, f.listMessagesErr
	}
	return f.messages[conversationID], nil
}

// conversationErrCode returns the *Error code, "" for nil, or "other" for a non-service error.
func conversationErrCode(err error) string {
	if err == nil {
		return ""
	}
	var se *Error
	if errors.As(err, &se) {
		return se.Code
	}
	return "other"
}

func TestConversationCreate(t *testing.T) {
	long200 := strings.Repeat("a", MaxNameLen)
	multi200 := strings.Repeat("é", MaxNameLen) // 200 runes, 400 bytes
	tests := []struct {
		name      string
		in        CreateConversationInput
		wantCode  string
		wantTitle string
		wantAgent string
	}{
		{name: "with title", in: CreateConversationInput{AgentID: "agent-1", Title: "hello"}, wantTitle: "hello", wantAgent: "agent-1"},
		{name: "empty title allowed", in: CreateConversationInput{AgentID: "agent-1"}, wantTitle: "", wantAgent: "agent-1"},
		{name: "agent id and title trimmed", in: CreateConversationInput{AgentID: "  agent-1 ", Title: "  hi  "}, wantTitle: "hi", wantAgent: "agent-1"},
		{name: "title at max length", in: CreateConversationInput{AgentID: "agent-1", Title: long200}, wantTitle: long200, wantAgent: "agent-1"},
		{name: "title at max runes", in: CreateConversationInput{AgentID: "agent-1", Title: multi200}, wantTitle: multi200, wantAgent: "agent-1"},
		{name: "empty agent id", in: CreateConversationInput{AgentID: "", Title: "x"}, wantCode: CodeInvalid},
		{name: "whitespace agent id", in: CreateConversationInput{AgentID: "   "}, wantCode: CodeInvalid},
		{name: "title too long", in: CreateConversationInput{AgentID: "agent-1", Title: long200 + "a"}, wantCode: CodeInvalid},
		{name: "unknown agent", in: CreateConversationInput{AgentID: "missing"}, wantCode: CodeNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			svc := NewConversationService(f)
			c, err := svc.Create(context.Background(), tc.in)
			if got := conversationErrCode(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
			if tc.wantCode != "" {
				if len(f.createdParams) != 0 {
					t.Errorf("store CreateConversation called on failed validation")
				}
				return
			}
			if c.AgentID != tc.wantAgent || c.Title != tc.wantTitle {
				t.Errorf("conversation = %+v, want agent %q title %q", c, tc.wantAgent, tc.wantTitle)
			}
			if len(f.createdParams) != 1 || f.createdParams[0].AgentID != tc.wantAgent || f.createdParams[0].Title != tc.wantTitle {
				t.Errorf("created params = %+v", f.createdParams)
			}
		})
	}
}

func TestConversationCreateStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		setup    func(f *conversationStoreFake)
		wantCode string
	}{
		{name: "agent lookup fails", setup: func(f *conversationStoreFake) { f.getAgentErr = boom }, wantCode: CodeInternal},
		{name: "create fails", setup: func(f *conversationStoreFake) { f.createErr = boom }, wantCode: CodeInternal},
		{name: "create ctx canceled passes through", setup: func(f *conversationStoreFake) { f.createErr = context.Canceled }, wantCode: "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			tc.setup(f)
			_, err := NewConversationService(f).Create(context.Background(), CreateConversationInput{AgentID: "agent-1"})
			if got := conversationErrCode(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
		})
	}
}

func TestConversationGet(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		setup    func(f *conversationStoreFake)
		wantCode string
	}{
		{name: "found", id: "conv-1", setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1", AgentID: "agent-1"}
		}},
		{name: "empty id", id: "", wantCode: CodeNotFound},
		{name: "unknown id", id: "nope", wantCode: CodeNotFound},
		{name: "store failure", id: "conv-1", setup: func(f *conversationStoreFake) { f.getConvErr = errors.New("boom") }, wantCode: CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			if tc.setup != nil {
				tc.setup(f)
			}
			c, err := NewConversationService(f).Get(context.Background(), tc.id)
			if got := conversationErrCode(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
			if tc.wantCode == "" && c.ID != tc.id {
				t.Errorf("id = %q, want %q", c.ID, tc.id)
			}
		})
	}
}

func TestConversationDelete(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		setup    func(f *conversationStoreFake)
		wantCode string
		wantGone bool
	}{
		{name: "deletes existing", id: "conv-1", setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1"}
		}, wantGone: true},
		{name: "empty id", id: "", wantCode: CodeNotFound},
		{name: "unknown id", id: "nope", wantCode: CodeNotFound},
		{name: "store failure", id: "conv-1", setup: func(f *conversationStoreFake) { f.deleteErr = errors.New("boom") }, wantCode: CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			if tc.setup != nil {
				tc.setup(f)
			}
			_, existed := f.conversations[tc.id]
			err := NewConversationService(f).Delete(context.Background(), tc.id)
			if got := conversationErrCode(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
			_, ok := f.conversations[tc.id]
			if tc.wantGone && (!existed || ok) {
				t.Errorf("conversation should have been deleted (existed=%v, present=%v)", existed, ok)
			}
			if !tc.wantGone && existed != ok {
				t.Errorf("conversation should be unchanged (existed=%v, present=%v)", existed, ok)
			}
		})
	}
}

func TestConversationList(t *testing.T) {
	tests := []struct {
		name       string
		agentID    string
		page       Page
		wantAgent  string
		wantLimit  int
		wantOffset int
		checkLen   bool
		wantLen    int
	}{
		{name: "no filter, defaults", agentID: "", page: Page{}, wantAgent: "", wantLimit: DefaultLimit, wantOffset: 0, checkLen: true, wantLen: 3},
		{name: "agent filter passed through trimmed", agentID: "  agent-1 ", page: Page{Limit: 10, Offset: 2}, wantAgent: "agent-1", wantLimit: 10, wantOffset: 2, checkLen: true, wantLen: 2},
		{name: "unknown agent still lists (no agent check)", agentID: "ghost", page: Page{Limit: 5}, wantAgent: "ghost", wantLimit: 5, checkLen: true, wantLen: 0},
		{name: "limit clamped to max", agentID: "", page: Page{Limit: 1000}, wantAgent: "", wantLimit: MaxLimit, checkLen: true, wantLen: 3},
		{name: "negative offset clamped", agentID: "", page: Page{Limit: 7, Offset: -4}, wantAgent: "", wantLimit: 7, wantOffset: 0, checkLen: true, wantLen: 3},
		{name: "negative limit defaults", agentID: "", page: Page{Limit: -1}, wantAgent: "", wantLimit: DefaultLimit, checkLen: true, wantLen: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			f.conversations["c1"] = queries.Conversation{ID: "c1", AgentID: "agent-1"}
			f.conversations["c2"] = queries.Conversation{ID: "c2", AgentID: "agent-1"}
			f.conversations["c3"] = queries.Conversation{ID: "c3", AgentID: "other"}
			cs, err := NewConversationService(f).List(context.Background(), tc.agentID, tc.page)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if f.lastListAgentID != tc.wantAgent {
				t.Errorf("store agent filter = %q, want %q", f.lastListAgentID, tc.wantAgent)
			}
			if f.lastListLimit != tc.wantLimit || f.lastListOffset != tc.wantOffset {
				t.Errorf("store limit/offset = %d/%d, want %d/%d", f.lastListLimit, f.lastListOffset, tc.wantLimit, tc.wantOffset)
			}
			if tc.checkLen && len(cs) != tc.wantLen {
				t.Errorf("len = %d, want %d", len(cs), tc.wantLen)
			}
		})
	}
}

func TestConversationListStoreFailure(t *testing.T) {
	f := newConversationStoreFake()
	f.listConvErr = errors.New("boom")
	_, err := NewConversationService(f).List(context.Background(), "", Page{})
	if got := conversationErrCode(err); got != CodeInternal {
		t.Fatalf("code = %q, want %q", got, CodeInternal)
	}
}

func TestConversationListMessages(t *testing.T) {
	tests := []struct {
		name       string
		convID     string
		page       Page
		setup      func(f *conversationStoreFake)
		wantCode   string
		wantLimit  int
		wantOffset int
		wantLen    int
	}{
		{name: "returns messages with defaults", convID: "conv-1", page: Page{}, setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1"}
			f.messages["conv-1"] = []queries.Message{{ID: "m1"}, {ID: "m2"}}
		}, wantLimit: DefaultLimit, wantLen: 2},
		{name: "pagination clamped", convID: "conv-1", page: Page{Limit: 500, Offset: -1}, setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1"}
		}, wantLimit: MaxLimit, wantOffset: 0, wantLen: 0},
		{name: "pagination passed through", convID: "conv-1", page: Page{Limit: 3, Offset: 6}, setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1"}
		}, wantLimit: 3, wantOffset: 6},
		{name: "empty id", convID: "", wantCode: CodeNotFound},
		{name: "unknown conversation", convID: "nope", wantCode: CodeNotFound},
		{name: "conversation lookup fails", convID: "conv-1", setup: func(f *conversationStoreFake) { f.getConvErr = errors.New("boom") }, wantCode: CodeInternal},
		{name: "message list fails", convID: "conv-1", setup: func(f *conversationStoreFake) {
			f.conversations["conv-1"] = queries.Conversation{ID: "conv-1"}
			f.listMessagesErr = errors.New("boom")
		}, wantCode: CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newConversationStoreFake()
			if tc.setup != nil {
				tc.setup(f)
			}
			ms, err := NewConversationService(f).ListMessages(context.Background(), tc.convID, tc.page)
			if got := conversationErrCode(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
			if tc.wantCode != "" {
				return
			}
			if f.lastMsgConversation != tc.convID || f.lastMsgLimit != tc.wantLimit || f.lastMsgOffset != tc.wantOffset {
				t.Errorf("store got conv=%q limit=%d offset=%d", f.lastMsgConversation, f.lastMsgLimit, f.lastMsgOffset)
			}
			if tc.wantLen != 0 && len(ms) != tc.wantLen {
				t.Errorf("len = %d, want %d", len(ms), tc.wantLen)
			}
		})
	}
}

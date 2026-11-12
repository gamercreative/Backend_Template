package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"alphahack/internal/queries"
)

// agentStoreFake is an in-memory AgentStore. err, when set, is returned by
// every method. Calls to ListAgents are recorded in listArgs.
type agentStoreFake struct {
	rows     map[string]queries.Agent
	order    []string
	nextID   int
	err      error
	listArgs [][2]int
	updates  []queries.UpdateAgentParams
}

func newAgentStoreFake() *agentStoreFake {
	return &agentStoreFake{rows: map[string]queries.Agent{}}
}

func (f *agentStoreFake) seed(a queries.Agent) queries.Agent {
	if a.ID == "" {
		f.nextID++
		a.ID = "agent-" + string(rune('0'+f.nextID))
	}
	f.rows[a.ID] = a
	f.order = append(f.order, a.ID)
	return a
}

func (f *agentStoreFake) CreateAgent(_ context.Context, p queries.CreateAgentParams) (queries.Agent, error) {
	if f.err != nil {
		return queries.Agent{}, f.err
	}
	return f.seed(queries.Agent{Name: p.Name, SystemPrompt: p.SystemPrompt, Model: p.Model}), nil
}

func (f *agentStoreFake) GetAgent(_ context.Context, id string) (queries.Agent, error) {
	if f.err != nil {
		return queries.Agent{}, f.err
	}
	a, ok := f.rows[id]
	if !ok {
		return queries.Agent{}, queries.ErrNotFound
	}
	return a, nil
}

func (f *agentStoreFake) ListAgents(_ context.Context, limit, offset int) ([]queries.Agent, error) {
	f.listArgs = append(f.listArgs, [2]int{limit, offset})
	if f.err != nil {
		return nil, f.err
	}
	out := []queries.Agent{}
	for _, id := range f.order {
		out = append(out, f.rows[id])
	}
	return out, nil
}

func (f *agentStoreFake) UpdateAgent(_ context.Context, p queries.UpdateAgentParams) (queries.Agent, error) {
	f.updates = append(f.updates, p)
	if f.err != nil {
		return queries.Agent{}, f.err
	}
	a, ok := f.rows[p.ID]
	if !ok {
		return queries.Agent{}, queries.ErrNotFound
	}
	a.Name, a.SystemPrompt, a.Model = p.Name, p.SystemPrompt, p.Model
	f.rows[p.ID] = a
	return a, nil
}

func (f *agentStoreFake) DeleteAgent(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.rows[id]; !ok {
		return queries.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

// agentErrCode returns the *Error code of err, or "" when err is nil or not a *Error.
func agentErrCode(err error) string {
	var se *Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func agentRepeat(r rune, n int) string {
	return strings.Repeat(string(r), n)
}

func TestAgentCreate(t *testing.T) {
	tests := []struct {
		name      string
		in        CreateAgentInput
		wantCode  string // "" = success
		wantName  string
		wantModel string
		wantPrmpt string
	}{
		{name: "happy path", in: CreateAgentInput{Name: "helper", SystemPrompt: "be kind", Model: "claude-x"}, wantName: "helper", wantModel: "claude-x", wantPrmpt: "be kind"},
		{name: "empty model allowed", in: CreateAgentInput{Name: "helper"}, wantName: "helper"},
		{name: "name trimmed", in: CreateAgentInput{Name: "  helper \n"}, wantName: "helper"},
		{name: "model trimmed", in: CreateAgentInput{Name: "a", Model: "  claude-x  "}, wantName: "a", wantModel: "claude-x"},
		{name: "prompt stored as given", in: CreateAgentInput{Name: "a", SystemPrompt: "  spaced  "}, wantName: "a", wantPrmpt: "  spaced  "},
		{name: "name at max length", in: CreateAgentInput{Name: agentRepeat('n', MaxNameLen)}, wantName: agentRepeat('n', MaxNameLen)},
		{name: "name counts runes not bytes", in: CreateAgentInput{Name: agentRepeat('é', MaxNameLen)}, wantName: agentRepeat('é', MaxNameLen)},
		{name: "prompt at max length", in: CreateAgentInput{Name: "a", SystemPrompt: agentRepeat('p', MaxPromptLen)}, wantName: "a", wantPrmpt: agentRepeat('p', MaxPromptLen)},
		{name: "model at max length", in: CreateAgentInput{Name: "a", Model: agentRepeat('m', MaxModelLen)}, wantName: "a", wantModel: agentRepeat('m', MaxModelLen)},
		{name: "empty name", in: CreateAgentInput{Name: ""}, wantCode: CodeInvalid},
		{name: "whitespace name", in: CreateAgentInput{Name: "   \t"}, wantCode: CodeInvalid},
		{name: "name too long", in: CreateAgentInput{Name: agentRepeat('n', MaxNameLen+1)}, wantCode: CodeInvalid},
		{name: "prompt too long", in: CreateAgentInput{Name: "a", SystemPrompt: agentRepeat('p', MaxPromptLen+1)}, wantCode: CodeInvalid},
		{name: "model too long", in: CreateAgentInput{Name: "a", Model: agentRepeat('m', MaxModelLen+1)}, wantCode: CodeInvalid},
		{name: "model with inner space", in: CreateAgentInput{Name: "a", Model: "claude x"}, wantCode: CodeInvalid},
		{name: "model with tab", in: CreateAgentInput{Name: "a", Model: "claude\tx"}, wantCode: CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAgentStoreFake()
			svc := NewAgentService(f)
			got, err := svc.Create(context.Background(), tt.in)
			if tt.wantCode != "" {
				if code := agentErrCode(err); code != tt.wantCode {
					t.Fatalf("code = %q (err %v), want %q", code, err, tt.wantCode)
				}
				if len(f.rows) != 0 {
					t.Fatalf("store was written on validation failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.wantName || got.Model != tt.wantModel || got.SystemPrompt != tt.wantPrmpt {
				t.Fatalf("got name=%q model=%q prompt=%q, want name=%q model=%q prompt=%q",
					got.Name, got.Model, got.SystemPrompt, tt.wantName, tt.wantModel, tt.wantPrmpt)
			}
			if got.ID == "" {
				t.Fatalf("created agent has no id")
			}
		})
	}
}

func TestAgentCreateStoreError(t *testing.T) {
	f := newAgentStoreFake()
	f.err = errors.New("boom: connection refused")
	_, err := NewAgentService(f).Create(context.Background(), CreateAgentInput{Name: "a"})
	if code := agentErrCode(err); code != CodeInternal {
		t.Fatalf("code = %q (err %v), want %q", code, err, CodeInternal)
	}
}

func TestAgentGet(t *testing.T) {
	f := newAgentStoreFake()
	seeded := f.seed(queries.Agent{Name: "a"})
	svc := NewAgentService(f)

	tests := []struct {
		name     string
		id       string
		wantCode string
	}{
		{name: "found", id: seeded.ID},
		{name: "empty id", id: "", wantCode: CodeNotFound},
		{name: "missing", id: "nope", wantCode: CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Get(context.Background(), tt.id)
			if code := agentErrCode(err); code != tt.wantCode {
				t.Fatalf("code = %q (err %v), want %q", code, err, tt.wantCode)
			}
			if tt.wantCode == "" && got.ID != seeded.ID {
				t.Fatalf("got id %q, want %q", got.ID, seeded.ID)
			}
		})
	}

	t.Run("store error", func(t *testing.T) {
		f.err = errors.New("db down")
		defer func() { f.err = nil }()
		_, err := svc.Get(context.Background(), seeded.ID)
		if code := agentErrCode(err); code != CodeInternal {
			t.Fatalf("code = %q, want %q", code, CodeInternal)
		}
	})
}

func TestAgentDelete(t *testing.T) {
	f := newAgentStoreFake()
	seeded := f.seed(queries.Agent{Name: "a"})
	svc := NewAgentService(f)

	if err := svc.Delete(context.Background(), ""); agentErrCode(err) != CodeNotFound {
		t.Fatalf("empty id: code = %q, want %q", agentErrCode(err), CodeNotFound)
	}
	if err := svc.Delete(context.Background(), "nope"); agentErrCode(err) != CodeNotFound {
		t.Fatalf("missing: code = %q, want %q", agentErrCode(err), CodeNotFound)
	}
	if err := svc.Delete(context.Background(), seeded.ID); err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	if _, ok := f.rows[seeded.ID]; ok {
		t.Fatalf("row still present after delete")
	}

	f2 := newAgentStoreFake()
	f2.err = errors.New("db down")
	if err := NewAgentService(f2).Delete(context.Background(), "x"); agentErrCode(err) != CodeInternal {
		t.Fatalf("store error: code = %q, want %q", agentErrCode(err), CodeInternal)
	}
}

func TestAgentList(t *testing.T) {
	tests := []struct {
		name     string
		page     Page
		wantArgs [2]int
	}{
		{name: "zero value uses default limit", page: Page{}, wantArgs: [2]int{DefaultLimit, 0}},
		{name: "limit above max is clamped", page: Page{Limit: 10_000, Offset: 5}, wantArgs: [2]int{MaxLimit, 5}},
		{name: "negative limit defaults", page: Page{Limit: -3}, wantArgs: [2]int{DefaultLimit, 0}},
		{name: "negative offset clamped to zero", page: Page{Limit: 10, Offset: -1}, wantArgs: [2]int{10, 0}},
		{name: "valid page passes through", page: Page{Limit: 7, Offset: 14}, wantArgs: [2]int{7, 14}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAgentStoreFake()
			f.seed(queries.Agent{Name: "a"})
			f.seed(queries.Agent{Name: "b"})
			got, err := NewAgentService(f).List(context.Background(), tt.page)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d agents, want 2", len(got))
			}
			if len(f.listArgs) != 1 || f.listArgs[0] != tt.wantArgs {
				t.Fatalf("store called with %v, want %v", f.listArgs, tt.wantArgs)
			}
		})
	}

	t.Run("store error", func(t *testing.T) {
		f := newAgentStoreFake()
		f.err = errors.New("db down")
		if _, err := NewAgentService(f).List(context.Background(), Page{}); agentErrCode(err) != CodeInternal {
			t.Fatalf("code = %q, want %q", agentErrCode(err), CodeInternal)
		}
	})
}

func TestAgentUpdate(t *testing.T) {
	str := func(s string) *string { return &s }

	type want struct {
		name, prompt, model string
	}
	base := queries.Agent{Name: "orig", SystemPrompt: "orig prompt", Model: "orig-model"}

	tests := []struct {
		name     string
		in       UpdateAgentInput
		wantCode string
		want     want
	}{
		{name: "all nil rejected", in: UpdateAgentInput{}, wantCode: CodeInvalid},
		{name: "name only leaves others", in: UpdateAgentInput{Name: str("new")}, want: want{"new", "orig prompt", "orig-model"}},
		{name: "prompt only leaves others", in: UpdateAgentInput{SystemPrompt: str("new prompt")}, want: want{"orig", "new prompt", "orig-model"}},
		{name: "model only leaves others", in: UpdateAgentInput{Model: str("new-model")}, want: want{"orig", "orig prompt", "new-model"}},
		{name: "empty prompt clears it", in: UpdateAgentInput{SystemPrompt: str("")}, want: want{"orig", "", "orig-model"}},
		{name: "empty model means default", in: UpdateAgentInput{Model: str("  ")}, want: want{"orig", "orig prompt", ""}},
		{name: "name trimmed", in: UpdateAgentInput{Name: str("  new  ")}, want: want{"new", "orig prompt", "orig-model"}},
		{name: "blank name rejected", in: UpdateAgentInput{Name: str("   ")}, wantCode: CodeInvalid},
		{name: "name too long rejected", in: UpdateAgentInput{Name: str(agentRepeat('n', MaxNameLen+1))}, wantCode: CodeInvalid},
		{name: "prompt too long rejected", in: UpdateAgentInput{SystemPrompt: str(agentRepeat('p', MaxPromptLen+1))}, wantCode: CodeInvalid},
		{name: "model with space rejected", in: UpdateAgentInput{Model: str("a b")}, wantCode: CodeInvalid},
		{name: "model too long rejected", in: UpdateAgentInput{Model: str(agentRepeat('m', MaxModelLen+1))}, wantCode: CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAgentStoreFake()
			seeded := f.seed(base)
			got, err := NewAgentService(f).Update(context.Background(), seeded.ID, tt.in)
			if tt.wantCode != "" {
				if code := agentErrCode(err); code != tt.wantCode {
					t.Fatalf("code = %q (err %v), want %q", code, err, tt.wantCode)
				}
				if len(f.updates) != 0 {
					t.Fatalf("UpdateAgent called on validation failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.name || got.SystemPrompt != tt.want.prompt || got.Model != tt.want.model {
				t.Fatalf("got name=%q prompt=%q model=%q, want %+v", got.Name, got.SystemPrompt, got.Model, tt.want)
			}
			if len(f.updates) != 1 || f.updates[0].ID != seeded.ID {
				t.Fatalf("UpdateAgent calls = %+v, want one for %s", f.updates, seeded.ID)
			}
		})
	}

	t.Run("empty id not found", func(t *testing.T) {
		_, err := NewAgentService(newAgentStoreFake()).Update(context.Background(), "", UpdateAgentInput{Name: str("x")})
		if agentErrCode(err) != CodeNotFound {
			t.Fatalf("code = %q, want %q", agentErrCode(err), CodeNotFound)
		}
	})

	t.Run("missing agent not found", func(t *testing.T) {
		_, err := NewAgentService(newAgentStoreFake()).Update(context.Background(), "nope", UpdateAgentInput{Name: str("x")})
		if agentErrCode(err) != CodeNotFound {
			t.Fatalf("code = %q, want %q", agentErrCode(err), CodeNotFound)
		}
	})

	t.Run("get store error is internal", func(t *testing.T) {
		f := newAgentStoreFake()
		seeded := f.seed(base)
		f.err = errors.New("db down")
		_, err := NewAgentService(f).Update(context.Background(), seeded.ID, UpdateAgentInput{Name: str("x")})
		if agentErrCode(err) != CodeInternal {
			t.Fatalf("code = %q, want %q", agentErrCode(err), CodeInternal)
		}
	})

	t.Run("update store error is internal", func(t *testing.T) {
		f := newAgentStoreFake()
		seeded := f.seed(base)
		updateFailer := &agentUpdateFailStore{agentStoreFake: f}
		_, err := NewAgentService(updateFailer).Update(context.Background(), seeded.ID, UpdateAgentInput{Name: str("x")})
		if agentErrCode(err) != CodeInternal {
			t.Fatalf("code = %q, want %q", agentErrCode(err), CodeInternal)
		}
	})
}

// agentUpdateFailStore fails only UpdateAgent, so the read path still succeeds.
type agentUpdateFailStore struct {
	*agentStoreFake
}

func (s *agentUpdateFailStore) UpdateAgent(_ context.Context, _ queries.UpdateAgentParams) (queries.Agent, error) {
	return queries.Agent{}, errors.New("write failed")
}

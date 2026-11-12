package queries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testQueries connects to TEST_DATABASE_URL (a migrated database) or skips.
func testQueries(t *testing.T) *Queries {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return New(pool)
}

var uniqueSeq = time.Now().UnixNano()

func uniq(prefix string) string {
	uniqueSeq++
	return fmt.Sprintf("qtest-%s-%d", prefix, uniqueSeq)
}

// newAgent creates an agent that is deleted (cascading to its children) at cleanup.
func newAgent(t *testing.T, q *Queries, name string) Agent {
	t.Helper()
	a, err := q.CreateAgent(context.Background(), CreateAgentParams{Name: name, SystemPrompt: "sys", Model: "m-1"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	t.Cleanup(func() { _ = q.DeleteAgent(context.Background(), a.ID) })
	return a
}

func newConversation(t *testing.T, q *Queries, agentID, title string) Conversation {
	t.Helper()
	c, err := q.CreateConversation(context.Background(), CreateConversationParams{AgentID: agentID, Title: title})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return c
}

func indexOfAgent(list []Agent, id string) int {
	for i, a := range list {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func TestIntegrationAgentsCRUD(t *testing.T) {
	q := testQueries(t)
	ctx := context.Background()

	a := newAgent(t, q, uniq("agent"))
	if a.ID == "" || a.Name == "" || a.CreatedAt.IsZero() {
		t.Fatalf("CreateAgent returned incomplete row: %+v", a)
	}

	got, err := q.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != a.Name || got.SystemPrompt != "sys" || got.Model != "m-1" {
		t.Errorf("GetAgent = %+v, want fields of %+v", got, a)
	}

	upd, err := q.UpdateAgent(ctx, UpdateAgentParams{ID: a.ID, Name: a.Name + "-x", SystemPrompt: "sys2", Model: "m-2"})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if upd.Name != a.Name+"-x" || upd.SystemPrompt != "sys2" || upd.Model != "m-2" {
		t.Errorf("UpdateAgent = %+v", upd)
	}
	if !upd.UpdatedAt.After(a.UpdatedAt) {
		t.Errorf("UpdateAgent did not advance updated_at: before %v after %v", a.UpdatedAt, upd.UpdatedAt)
	}

	// Ordering: created_at DESC, so a newer agent lists before an older one.
	b := newAgent(t, q, uniq("agent-newer"))
	list, err := q.ListAgents(ctx, 1000, 0)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	ia, ib := indexOfAgent(list, a.ID), indexOfAgent(list, b.ID)
	if ia < 0 || ib < 0 || ib >= ia {
		t.Errorf("ListAgents order: newer agent at %d, older at %d; want newer first", ib, ia)
	}

	if err := q.DeleteAgent(ctx, b.ID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if err := q.DeleteAgent(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteAgent err = %v, want ErrNotFound", err)
	}
}

func TestIntegrationAgentNotFound(t *testing.T) {
	q := testQueries(t)
	ctx := context.Background()
	missing := uniq("missing-agent-id")

	if _, err := q.GetAgent(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAgent err = %v, want ErrNotFound", err)
	}
	if _, err := q.UpdateAgent(ctx, UpdateAgentParams{ID: missing, Name: "n"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateAgent err = %v, want ErrNotFound", err)
	}
	if err := q.DeleteAgent(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteAgent err = %v, want ErrNotFound", err)
	}
}

func TestIntegrationConversations(t *testing.T) {
	q := testQueries(t)
	ctx := context.Background()

	a := newAgent(t, q, uniq("agent-conv"))
	other := newAgent(t, q, uniq("agent-other"))
	empty := newAgent(t, q, uniq("agent-empty"))

	c1 := newConversation(t, q, a.ID, "first")
	c2 := newConversation(t, q, a.ID, "second")
	newConversation(t, q, other.ID, "elsewhere")

	got, err := q.GetConversation(ctx, c1.ID)
	if err != nil || got.AgentID != a.ID || got.Title != "first" {
		t.Fatalf("GetConversation = %+v, %v", got, err)
	}

	// Filtered by agent, updated_at DESC: c2 was created last, so it comes first.
	list, err := q.ListConversations(ctx, a.ID, 100, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 2 || list[0].ID != c2.ID || list[1].ID != c1.ID {
		t.Fatalf("ListConversations(agent) = %v, want [second first]", titles(list))
	}

	// Touch c1: it becomes the most recently updated.
	if err := q.TouchConversation(ctx, c1.ID); err != nil {
		t.Fatalf("TouchConversation: %v", err)
	}
	list, err = q.ListConversations(ctx, a.ID, 100, 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 2 || list[0].ID != c1.ID {
		t.Errorf("after touch, ListConversations(agent) = %v, want [first second]", titles(list))
	}

	// Empty agent id means all agents; paging applies.
	all, err := q.ListConversations(ctx, "", 1000, 0)
	if err != nil {
		t.Fatalf("ListConversations(all): %v", err)
	}
	if len(all) < 3 {
		t.Errorf("ListConversations(all) returned %d rows, want at least 3", len(all))
	}
	page, err := q.ListConversations(ctx, a.ID, 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != c2.ID {
		t.Errorf("ListConversations page 2 = %v, %v; want [second]", titles(page), err)
	}

	// Agent with no conversations gets a non-nil empty slice.
	none, err := q.ListConversations(ctx, empty.ID, 10, 0)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListConversations(empty) = %#v, %v; want non-nil empty slice", none, err)
	}

	if err := q.DeleteConversation(ctx, c2.ID); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := q.GetConversation(ctx, c2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetConversation after delete err = %v, want ErrNotFound", err)
	}
	if err := q.DeleteConversation(ctx, c2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteConversation err = %v, want ErrNotFound", err)
	}
	if err := q.TouchConversation(ctx, c2.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchConversation(deleted) err = %v, want ErrNotFound", err)
	}
	if _, err := q.GetConversation(ctx, uniq("missing-conv")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetConversation(missing) err = %v, want ErrNotFound", err)
	}
}

func titles(cs []Conversation) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Title)
	}
	return out
}

func TestIntegrationRunsMessagesToolCalls(t *testing.T) {
	q := testQueries(t)
	ctx := context.Background()

	a := newAgent(t, q, uniq("agent-trace"))
	conv := newConversation(t, q, a.ID, "trace")

	// Runs.
	run, err := q.CreateRun(ctx, CreateRunParams{ConversationID: conv.ID, Model: "m-1"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != RunStatusRunning || run.FinishedAt != nil {
		t.Errorf("new run = %+v, want running with nil finished_at", run)
	}
	finished, err := q.FinishRun(ctx, FinishRunParams{
		ID: run.ID, Status: RunStatusCompleted, StopReason: "end_turn", Model: "m-1",
		InputTokens: 12, OutputTokens: 34, Iterations: 2,
	})
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if finished.Status != RunStatusCompleted || finished.FinishedAt == nil || finished.InputTokens != 12 ||
		finished.OutputTokens != 34 || finished.Iterations != 2 || finished.StopReason != "end_turn" {
		t.Errorf("FinishRun = %+v", finished)
	}
	if _, err := q.FinishRun(ctx, FinishRunParams{ID: uniq("missing-run"), Status: RunStatusFailed}); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishRun(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := q.GetRun(ctx, uniq("missing-run")); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun(missing) err = %v, want ErrNotFound", err)
	}
	gotRun, err := q.GetRun(ctx, run.ID)
	if err != nil || gotRun.Status != RunStatusCompleted {
		t.Errorf("GetRun = %+v, %v", gotRun, err)
	}
	run2, err := q.CreateRun(ctx, CreateRunParams{ConversationID: conv.ID, Model: "m-1"})
	if err != nil {
		t.Fatalf("CreateRun 2: %v", err)
	}
	runs, err := q.ListRuns(ctx, conv.ID, 10, 0)
	if err != nil || len(runs) != 2 || runs[0].ID != run2.ID {
		t.Errorf("ListRuns = %d rows, %v; want newest first", len(runs), err)
	}
	noRuns, err := q.ListRuns(ctx, newConversation(t, q, a.ID, "empty").ID, 10, 0)
	if err != nil || noRuns == nil || len(noRuns) != 0 {
		t.Errorf("ListRuns(empty) = %#v, %v; want non-nil empty slice", noRuns, err)
	}

	// Messages: user (no run), assistant (linked to run), user.
	m1, err := q.CreateMessage(ctx, CreateMessageParams{ConversationID: conv.ID, Role: RoleUser, Content: "one"})
	if err != nil {
		t.Fatalf("CreateMessage 1: %v", err)
	}
	if m1.RunID != nil {
		t.Errorf("message without run has RunID %v", *m1.RunID)
	}
	m2, err := q.CreateMessage(ctx, CreateMessageParams{ConversationID: conv.ID, RunID: &run.ID, Role: RoleAssistant, Content: "two"})
	if err != nil {
		t.Fatalf("CreateMessage 2: %v", err)
	}
	if m2.RunID == nil || *m2.RunID != run.ID {
		t.Errorf("message RunID = %v, want %s", m2.RunID, run.ID)
	}
	m3, err := q.CreateMessage(ctx, CreateMessageParams{ConversationID: conv.ID, Role: RoleUser, Content: "three"})
	if err != nil {
		t.Fatalf("CreateMessage 3: %v", err)
	}

	msgs, err := q.ListMessages(ctx, conv.ID, 100, 0)
	if err != nil || len(msgs) != 3 || msgs[0].ID != m1.ID || msgs[1].ID != m2.ID || msgs[2].ID != m3.ID {
		t.Errorf("ListMessages = %d rows, %v; want insertion order", len(msgs), err)
	}
	recent, err := q.ListRecentMessages(ctx, conv.ID, 2)
	if err != nil || len(recent) != 2 || recent[0].ID != m2.ID || recent[1].ID != m3.ID {
		t.Errorf("ListRecentMessages(2) = %v, %v; want [two three] oldest first", contents(recent), err)
	}
	noMsgs, err := q.ListMessages(ctx, newConversation(t, q, a.ID, "no-msgs").ID, 10, 0)
	if err != nil || noMsgs == nil || len(noMsgs) != 0 {
		t.Errorf("ListMessages(empty) = %#v, %v; want non-nil empty slice", noMsgs, err)
	}
	noRecent, err := q.ListRecentMessages(ctx, newConversation(t, q, a.ID, "no-recent").ID, 5)
	if err != nil || noRecent == nil || len(noRecent) != 0 {
		t.Errorf("ListRecentMessages(empty) = %#v, %v; want non-nil empty slice", noRecent, err)
	}

	// Tool calls: empty input is stored as {}, and the list is seq ASC.
	tc1, err := q.CreateToolCall(ctx, CreateToolCallParams{RunID: run.ID, Name: "noop", Output: "ok", DurationMS: 5})
	if err != nil {
		t.Fatalf("CreateToolCall 1: %v", err)
	}
	if string(tc1.Input) != "{}" {
		t.Errorf("empty input stored as %s, want {}", tc1.Input)
	}
	tc2, err := q.CreateToolCall(ctx, CreateToolCallParams{
		RunID: run.ID, Name: "lookup", Input: json.RawMessage(`{"q": 1}`), Output: "bad", IsError: true, DurationMS: 7,
	})
	if err != nil {
		t.Fatalf("CreateToolCall 2: %v", err)
	}
	if !tc2.IsError || tc2.DurationMS != 7 {
		t.Errorf("CreateToolCall 2 = %+v", tc2)
	}
	var in map[string]int
	if err := json.Unmarshal(tc2.Input, &in); err != nil || in["q"] != 1 {
		t.Errorf("tool call input = %s, %v", tc2.Input, err)
	}
	calls, err := q.ListToolCalls(ctx, run.ID)
	if err != nil || len(calls) != 2 || calls[0].ID != tc1.ID || calls[1].ID != tc2.ID {
		t.Errorf("ListToolCalls = %d rows, %v; want insertion order", len(calls), err)
	}
	noCalls, err := q.ListToolCalls(ctx, run2.ID)
	if err != nil || noCalls == nil || len(noCalls) != 0 {
		t.Errorf("ListToolCalls(empty) = %#v, %v; want non-nil empty slice", noCalls, err)
	}
}

func contents(ms []Message) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Content)
	}
	return out
}

func TestIntegrationInTx(t *testing.T) {
	q := testQueries(t)
	ctx := context.Background()

	t.Run("commit", func(t *testing.T) {
		var id string
		err := q.InTx(ctx, func(tx Querier) error {
			a, err := tx.CreateAgent(ctx, CreateAgentParams{Name: uniq("tx-commit")})
			if err != nil {
				return err
			}
			id = a.ID
			return nil
		})
		if err != nil {
			t.Fatalf("InTx: %v", err)
		}
		if err := q.DeleteAgent(ctx, id); err != nil {
			t.Errorf("agent from committed tx not visible: %v", err)
		}
	})

	t.Run("rollback on error", func(t *testing.T) {
		var id string
		sentinel := errors.New("boom")
		err := q.InTx(ctx, func(tx Querier) error {
			a, err := tx.CreateAgent(ctx, CreateAgentParams{Name: uniq("tx-err")})
			if err != nil {
				return err
			}
			id = a.ID
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("InTx err = %v, want sentinel", err)
		}
		if _, err := q.GetAgent(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("agent from rolled-back tx: GetAgent err = %v, want ErrNotFound", err)
		}
	})

	t.Run("rollback on panic", func(t *testing.T) {
		var id string
		func() {
			defer func() {
				if r := recover(); r != "boom" {
					t.Errorf("recover() = %v, want re-panic with boom", r)
				}
			}()
			_ = q.InTx(ctx, func(tx Querier) error {
				a, err := tx.CreateAgent(ctx, CreateAgentParams{Name: uniq("tx-panic")})
				if err != nil {
					return err
				}
				id = a.ID
				panic("boom")
			})
		}()
		if _, err := q.GetAgent(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("agent from panicked tx: GetAgent err = %v, want ErrNotFound", err)
		}
	})
}

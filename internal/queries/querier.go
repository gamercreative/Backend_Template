// Package queries is the only package that talks to PostgreSQL.
//
// Contract (owned by the orchestrator, do not change signatures):
//   - Every SQL statement lives in its own file internal/queries/sql/<method_snake_case>.sql
//     (e.g. CreateAgent -> sql/create_agent.sql), embedded with //go:embed. One statement
//     per file, positional params ($1, $2 ...), no string concatenation.
//   - scripts/check_queries.sh PREPAREs every one of those files against the live schema.
//   - Get*/Update*/Delete*/Finish* return ErrNotFound when no row matches.
//   - List* return a non-nil empty slice when there are no rows (so JSON is [] not null).
//   - No business rules here: no validation, no defaults beyond what the schema provides.
package queries

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Querier is the full data-access surface. *Queries (constructed with
// New(db DBTX) *Queries) must satisfy it:
//
//	var _ Querier = (*Queries)(nil)
type Querier interface {
	// Ping runs a trivial query to prove the database is reachable.
	Ping(ctx context.Context) error

	// InTx runs fn inside a transaction; the Querier passed to fn is bound to
	// that transaction. Commit when fn returns nil, roll back otherwise
	// (including on panic, which is re-raised).
	InTx(ctx context.Context, fn func(q Querier) error) error

	// Agents. ListAgents orders by created_at DESC, id.
	CreateAgent(ctx context.Context, p CreateAgentParams) (Agent, error)
	GetAgent(ctx context.Context, id string) (Agent, error)
	ListAgents(ctx context.Context, limit, offset int) ([]Agent, error)
	UpdateAgent(ctx context.Context, p UpdateAgentParams) (Agent, error) // also sets updated_at = now()
	DeleteAgent(ctx context.Context, id string) error

	// Conversations. ListConversations orders by updated_at DESC, id; an empty
	// agentID means "all agents".
	CreateConversation(ctx context.Context, p CreateConversationParams) (Conversation, error)
	GetConversation(ctx context.Context, id string) (Conversation, error)
	ListConversations(ctx context.Context, agentID string, limit, offset int) ([]Conversation, error)
	TouchConversation(ctx context.Context, id string) error // updated_at = now()
	DeleteConversation(ctx context.Context, id string) error

	// Messages. ListMessages orders by seq ASC (oldest first).
	// ListRecentMessages returns the newest `limit` messages, still ordered seq ASC.
	CreateMessage(ctx context.Context, p CreateMessageParams) (Message, error)
	ListMessages(ctx context.Context, conversationID string, limit, offset int) ([]Message, error)
	ListRecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error)

	// Runs. ListRuns orders by started_at DESC, id.
	CreateRun(ctx context.Context, p CreateRunParams) (Run, error)
	FinishRun(ctx context.Context, p FinishRunParams) (Run, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, conversationID string, limit, offset int) ([]Run, error)

	// Tool calls. ListToolCalls orders by seq ASC.
	CreateToolCall(ctx context.Context, p CreateToolCallParams) (ToolCall, error)
	ListToolCalls(ctx context.Context, runID string) ([]ToolCall, error)
}

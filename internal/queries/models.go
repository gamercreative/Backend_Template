package queries

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned by Get*/Update*/Delete*/Finish* when no row matches.
var ErrNotFound = errors.New("queries: not found")

// Run statuses (runs.status).
const (
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
)

// Message roles (messages.role).
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Row types. These are returned as-is in API responses, so the json tags are
// part of the HTTP contract (see openapi.yaml).

type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SystemPrompt string    `json:"system_prompt"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Conversation struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	RunID          *string   `json:"run_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
}

type Run struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	Status         string     `json:"status"`
	StopReason     string     `json:"stop_reason"`
	Error          string     `json:"error"`
	Model          string     `json:"model"`
	InputTokens    int64      `json:"input_tokens"`
	OutputTokens   int64      `json:"output_tokens"`
	Iterations     int        `json:"iterations"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

type ToolCall struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	Output     string          `json:"output"`
	IsError    bool            `json:"is_error"`
	DurationMS int             `json:"duration_ms"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Parameter structs.

type CreateAgentParams struct {
	Name         string
	SystemPrompt string
	Model        string
}

// UpdateAgentParams is a full replace of the mutable columns; the service
// merges PATCH input with the current row before calling UpdateAgent.
type UpdateAgentParams struct {
	ID           string
	Name         string
	SystemPrompt string
	Model        string
}

type CreateConversationParams struct {
	AgentID string
	Title   string
}

type CreateMessageParams struct {
	ConversationID string
	RunID          *string
	Role           string
	Content        string
}

type CreateRunParams struct {
	ConversationID string
	Model          string
}

// FinishRunParams sets the terminal state of a run and stamps finished_at = now().
type FinishRunParams struct {
	ID           string
	Status       string
	StopReason   string
	Error        string
	Model        string
	InputTokens  int64
	OutputTokens int64
	Iterations   int
}

type CreateToolCallParams struct {
	RunID      string
	Name       string
	Input      json.RawMessage
	Output     string
	IsError    bool
	DurationMS int
}

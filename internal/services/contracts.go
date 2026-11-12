// Package services holds all business rules. Handlers call services; services
// call queries (through the narrow Store interfaces below) and ai.
//
// Contract (owned by the orchestrator, do not change). Workers implement:
//
//	func NewAgentService(store AgentStore) AgentService                                 // agents.go
//	func NewConversationService(store ConversationStore) ConversationService            // conversations.go
//	func NewChatService(store ChatStore, client ai.Client, tools []ai.Tool, opts ChatOptions) ChatService // chat.go
//	func DefaultTools() []ai.Tool                                                        // tools.go
//
// Every error a service returns to a handler must be a *Error (use the
// constructors below) or a context error passed through untouched.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alphahack/internal/queries"
)

// ---- errors ----------------------------------------------------------------

const (
	CodeInvalid  = "validation_failed" // 400
	CodeNotFound = "not_found"         // 404
	CodeConflict = "conflict"          // 409
	CodeUpstream = "upstream_error"    // 502: the model provider failed
	CodeInternal = "internal_error"    // 500
)

// Error is the only error type that crosses the service boundary.
// Message is safe to show to API clients; Err is logged, never shown.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

func Invalid(format string, args ...any) *Error {
	return &Error{Code: CodeInvalid, Message: fmt.Sprintf(format, args...)}
}

func NotFound(resource string) *Error {
	return &Error{Code: CodeNotFound, Message: resource + " not found"}
}

func Conflict(message string) *Error {
	return &Error{Code: CodeConflict, Message: message}
}

func Upstream(err error) *Error {
	return &Error{Code: CodeUpstream, Message: "the model provider request failed", Err: err}
}

func Internal(err error) *Error {
	return &Error{Code: CodeInternal, Message: "internal error", Err: err}
}

// FromStore converts an error coming out of the queries layer: ErrNotFound
// becomes NotFound(resource), context errors pass through, anything else is Internal.
func FromStore(err error, resource string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, queries.ErrNotFound):
		return NotFound(resource)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return Internal(err)
	}
}

// ---- pagination ------------------------------------------------------------

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is limit/offset pagination. Handlers parse it; services call Normalize.
type Page struct {
	Limit  int
	Offset int
}

// Normalize applies the default and clamps to [1, MaxLimit] / offset >= 0.
func (p Page) Normalize() Page {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

// clean trims whitespace; shared by validators.
func clean(s string) string { return strings.TrimSpace(s) }

// ---- health ----------------------------------------------------------------

type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthService interface {
	Check(ctx context.Context) error
}

type healthService struct{ db Pinger }

func NewHealthService(db Pinger) HealthService { return &healthService{db: db} }

func (s *healthService) Check(ctx context.Context) error {
	if err := s.db.Ping(ctx); err != nil {
		return &Error{Code: CodeInternal, Message: "database unreachable", Err: err}
	}
	return nil
}

// ---- agents ----------------------------------------------------------------

// Limits enforced by the services.
const (
	MaxNameLen    = 200
	MaxPromptLen  = 100_000
	MaxContentLen = 100_000
)

type CreateAgentInput struct {
	Name         string `json:"name"`          // required, 1..MaxNameLen after trimming
	SystemPrompt string `json:"system_prompt"` // optional, <= MaxPromptLen
	Model        string `json:"model"`         // optional; empty = server default model
}

// UpdateAgentInput is a PATCH: nil fields are left unchanged. At least one
// field must be set.
type UpdateAgentInput struct {
	Name         *string `json:"name"`
	SystemPrompt *string `json:"system_prompt"`
	Model        *string `json:"model"`
}

type AgentService interface {
	Create(ctx context.Context, in CreateAgentInput) (queries.Agent, error)
	Get(ctx context.Context, id string) (queries.Agent, error)
	List(ctx context.Context, p Page) ([]queries.Agent, error)
	Update(ctx context.Context, id string, in UpdateAgentInput) (queries.Agent, error)
	Delete(ctx context.Context, id string) error
}

type AgentStore interface {
	CreateAgent(ctx context.Context, p queries.CreateAgentParams) (queries.Agent, error)
	GetAgent(ctx context.Context, id string) (queries.Agent, error)
	ListAgents(ctx context.Context, limit, offset int) ([]queries.Agent, error)
	UpdateAgent(ctx context.Context, p queries.UpdateAgentParams) (queries.Agent, error)
	DeleteAgent(ctx context.Context, id string) error
}

// ---- conversations ---------------------------------------------------------

type CreateConversationInput struct {
	AgentID string `json:"agent_id"` // required; must exist (else NotFound("agent"))
	Title   string `json:"title"`    // optional, <= MaxNameLen
}

type ConversationService interface {
	Create(ctx context.Context, in CreateConversationInput) (queries.Conversation, error)
	Get(ctx context.Context, id string) (queries.Conversation, error)
	// List filters by agentID when it is non-empty.
	List(ctx context.Context, agentID string, p Page) ([]queries.Conversation, error)
	Delete(ctx context.Context, id string) error
	// ListMessages returns NotFound("conversation") when the conversation does not exist.
	ListMessages(ctx context.Context, conversationID string, p Page) ([]queries.Message, error)
}

type ConversationStore interface {
	GetAgent(ctx context.Context, id string) (queries.Agent, error)
	CreateConversation(ctx context.Context, p queries.CreateConversationParams) (queries.Conversation, error)
	GetConversation(ctx context.Context, id string) (queries.Conversation, error)
	ListConversations(ctx context.Context, agentID string, limit, offset int) ([]queries.Conversation, error)
	DeleteConversation(ctx context.Context, id string) error
	ListMessages(ctx context.Context, conversationID string, limit, offset int) ([]queries.Message, error)
}

// ---- chat (the agent loop) -------------------------------------------------

type SendMessageInput struct {
	Content string `json:"content"` // required, 1..MaxContentLen after trimming
}

// ChatResult is the response to one user turn.
type ChatResult struct {
	UserMessage      queries.Message    `json:"user_message"`
	AssistantMessage queries.Message    `json:"assistant_message"`
	Run              queries.Run        `json:"run"`
	ToolCalls        []queries.ToolCall `json:"tool_calls"`
}

// RunDetail is a run plus its tool-call trace (JSON is the run's fields
// flattened, with an extra "tool_calls" array).
type RunDetail struct {
	queries.Run
	ToolCalls []queries.ToolCall `json:"tool_calls"`
}

// ToolInfo describes a registered tool for GET /v1/tools.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ChatOptions struct {
	DefaultModel  string // used when the agent's model is empty
	HistoryLimit  int    // max prior messages sent to the model; <= 0 = 40
	MaxIterations int    // passed to ai.Request; <= 0 = client default
}

// Chat stream event names (ChatEvent.Type). They are the SSE event names too.
const (
	ChatEventStart      = "start"       // Data: ChatStartData
	ChatEventDelta      = "delta"       // Data: ChatTextData (assistant text chunk)
	ChatEventThinking   = "thinking"    // Data: ChatTextData (model reasoning chunk)
	ChatEventToolCall   = "tool_call"   // Data: ChatToolData (name + input)
	ChatEventToolResult = "tool_result" // Data: ChatToolData (complete)
)

// ChatEvent is one event of a streamed turn. Data is JSON-marshalable.
type ChatEvent struct {
	Type string
	Data any
}

type ChatStartData struct {
	Run         queries.Run     `json:"run"`
	UserMessage queries.Message `json:"user_message"`
}

type ChatTextData struct {
	Text string `json:"text"`
}

type ChatToolData struct {
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	Output     string          `json:"output"`
	IsError    bool            `json:"is_error"`
	DurationMS int             `json:"duration_ms"`
}

type ChatService interface {
	// SendMessage stores the user message, runs the agent loop and stores the
	// assistant reply plus the tool-call trace. See AGENTS.md "Chat flow".
	SendMessage(ctx context.Context, conversationID string, in SendMessageInput) (*ChatResult, error)
	// StreamMessage is SendMessage with live events. emit is called
	// synchronously and in order: one ChatEventStart once the run and user
	// message are stored, then delta / thinking / tool_call / tool_result as
	// the model produces them. Validation and not-found errors are returned
	// before any event is emitted. The final result and any later error are
	// returned exactly as SendMessage returns them (not emitted).
	StreamMessage(ctx context.Context, conversationID string, in SendMessageInput, emit func(ChatEvent)) (*ChatResult, error)
	GetRun(ctx context.Context, id string) (*RunDetail, error)
	// ListRuns returns NotFound("conversation") when the conversation does not exist.
	ListRuns(ctx context.Context, conversationID string, p Page) ([]queries.Run, error)
	ListTools() []ToolInfo
}

type ChatStore interface {
	InTx(ctx context.Context, fn func(q queries.Querier) error) error
	GetAgent(ctx context.Context, id string) (queries.Agent, error)
	GetConversation(ctx context.Context, id string) (queries.Conversation, error)
	TouchConversation(ctx context.Context, id string) error
	CreateMessage(ctx context.Context, p queries.CreateMessageParams) (queries.Message, error)
	ListRecentMessages(ctx context.Context, conversationID string, limit int) ([]queries.Message, error)
	CreateRun(ctx context.Context, p queries.CreateRunParams) (queries.Run, error)
	FinishRun(ctx context.Context, p queries.FinishRunParams) (queries.Run, error)
	GetRun(ctx context.Context, id string) (queries.Run, error)
	ListRuns(ctx context.Context, conversationID string, limit, offset int) ([]queries.Run, error)
	CreateToolCall(ctx context.Context, p queries.CreateToolCallParams) (queries.ToolCall, error)
	ListToolCalls(ctx context.Context, runID string) ([]queries.ToolCall, error)
}

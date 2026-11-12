// Package ai is the only package that talks to the model provider.
//
// Contract (owned by the orchestrator, do not change):
//
//	func NewAnthropic(opts Options) Client    // Anthropic API, official Go SDK
//	func NewOllama(opts OllamaOptions) Client // local models through Ollama's /api/chat
//	func NewFake() Client                     // deterministic offline client for dev/tests
//
// Client.Run drives the whole agent loop for one user turn: call the model,
// execute any tools it asks for via Tool.Handler, feed results back, repeat
// until the model stops asking for tools or MaxIterations is reached.
package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one prior turn of plain-text conversation history.
type Message struct {
	Role    Role
	Content string
}

// ToolHandler executes a tool. input is the raw JSON object the model produced.
// A returned error is reported back to the model as an error tool result; it
// does not abort the run.
type ToolHandler func(ctx context.Context, input json.RawMessage) (string, error)

// Tool is a function the model may call. Properties/Required are the
// "properties" and "required" members of a JSON Schema object.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
	Handler     ToolHandler
}

// ToolCall is the trace of one executed tool call.
type ToolCall struct {
	Name     string
	Input    json.RawMessage
	Output   string
	IsError  bool
	Duration time.Duration
}

// EventType names a streaming event emitted during Run.
type EventType string

const (
	EventTextDelta     EventType = "text_delta"     // Text: a chunk of assistant text
	EventThinkingDelta EventType = "thinking_delta" // Text: a chunk of model reasoning, when the provider exposes it
	EventToolCall      EventType = "tool_call"      // Tool: Name and Input set; the tool is about to run
	EventToolResult    EventType = "tool_result"    // Tool: complete (Output, IsError, Duration)
)

// Event is one streaming event. Exactly one of Text / Tool is set, by Type.
type Event struct {
	Type EventType
	Text string
	Tool *ToolCall
}

// Request is one agent turn. Messages must be non-empty and end with a user message.
type Request struct {
	Model         string // empty = the client's default model
	System        string
	Messages      []Message
	Tools         []Tool
	MaxIterations int // <= 0 = the client's default

	// OnEvent, when non-nil, turns on streaming: the client streams each model
	// call and invokes OnEvent synchronously, in order, from the goroutine
	// that called Run. Text deltas of every model call are emitted, including
	// text that precedes tool calls; Result.Text still holds only the final
	// call's text. A client that cannot stream emits the text as one delta.
	// Tool events come from execTool (loop.go). OnEvent must not block for long.
	OnEvent func(Event)
}

// Stop reasons the service layer cares about. Provider values are passed
// through verbatim (end_turn, max_tokens, refusal, ...).
const (
	StopEndTurn       = "end_turn"
	StopMaxTokens     = "max_tokens"
	StopRefusal       = "refusal"
	StopMaxIterations = "max_iterations" // loop cut off by MaxIterations
)

// Result is the outcome of a run. Token counts are summed over all iterations.
type Result struct {
	Text         string // final assistant text (text blocks of the last response, joined)
	StopReason   string
	Model        string // model that actually served the final response
	InputTokens  int64
	OutputTokens int64
	Iterations   int // number of model calls made
	ToolCalls    []ToolCall
}

// Client runs agent turns.
//
// On failure Run returns a non-nil error AND, when anything was accumulated
// before the failure, a non-nil partial *Result (tool calls, usage, iterations)
// so the caller can still persist the trace. Context errors are returned
// unwrapped-compatible (errors.Is(err, context.DeadlineExceeded) works).
type Client interface {
	Run(ctx context.Context, req Request) (*Result, error)
}

// Options configures NewAnthropic.
type Options struct {
	APIKey        string // empty = let the SDK resolve credentials from the environment
	Model         string // default model, e.g. "claude-opus-5-5"
	Effort        string // "", low, medium, high, xhigh, max; "" = omit (model default)
	MaxTokens     int64  // per model call; <= 0 = 16000
	MaxIterations int    // <= 0 = 10
	Fallbacks     bool   // opt in to server-side refusal fallbacks where the model supports it
}

// OllamaOptions configures NewOllama (Ollama's native POST /api/chat, NDJSON streaming).
type OllamaOptions struct {
	BaseURL       string       // "" = http://localhost:11434
	Model         string       // default model, e.g. "qwen3.5:35b-a3b"
	Think         string       // "" = omit the field (model default), "true" or "false"
	NumCtx        int          // context window in tokens; <= 0 = omit (server default)
	MaxTokens     int64        // options.num_predict; <= 0 = omit
	MaxIterations int          // <= 0 = 10
	HTTPClient    *http.Client // nil = a client with no overall timeout (the context bounds the call)
}

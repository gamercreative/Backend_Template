package ai

// Helpers shared by every Client implementation. Owned by the orchestrator:
// signatures here are frozen.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// validateRequest checks the Request invariants shared by every Client.
func validateRequest(req Request) error {
	if len(req.Messages) == 0 {
		return errors.New("ai: request has no messages")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != RoleUser {
		return fmt.Errorf("ai: last message must be from the user, got %q", last.Role)
	}
	if strings.TrimSpace(last.Content) == "" {
		return errors.New("ai: last user message is empty")
	}
	for i, m := range req.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("ai: message %d has invalid role %q", i, m.Role)
		}
	}
	for i, t := range req.Tools {
		if t.Name == "" || t.Handler == nil {
			return fmt.Errorf("ai: tool %d needs a name and a handler", i)
		}
	}
	return nil
}

// emit sends e to the request's OnEvent callback, if any.
func (r Request) emit(e Event) {
	if r.OnEvent != nil {
		r.OnEvent(e)
	}
}

// toolHandlers indexes the request's tools by name.
func toolHandlers(tools []Tool) map[string]ToolHandler {
	m := make(map[string]ToolHandler, len(tools))
	for _, t := range tools {
		m[t.Name] = t.Handler
	}
	return m
}

// execTool runs one tool call and emits EventToolCall before and
// EventToolResult after it. Use this from every client's tool loop.
func execTool(ctx context.Context, req Request, handlers map[string]ToolHandler, name string, input json.RawMessage) ToolCall {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	req.emit(Event{Type: EventToolCall, Tool: &ToolCall{Name: name, Input: input}})
	call := runTool(ctx, handlers, name, input)
	done := call
	req.emit(Event{Type: EventToolResult, Tool: &done})
	return call
}

// runTool executes one tool call, converting unknown tools, invalid JSON input,
// handler errors and panics into error results. It never emits events.
func runTool(ctx context.Context, handlers map[string]ToolHandler, name string, input json.RawMessage) (call ToolCall) {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	call = ToolCall{Name: name, Input: input}
	h, ok := handlers[name]
	if !ok {
		call.Output = fmt.Sprintf("unknown tool %q", name)
		call.IsError = true
		return call
	}
	if !json.Valid(input) {
		// Streamed tool input can arrive truncated; never hand that to a handler.
		call.Input = json.RawMessage("{}")
		call.Output = "invalid JSON input"
		call.IsError = true
		return call
	}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if r := recover(); r != nil {
			call.Output = fmt.Sprintf("tool %q panicked: %v", name, r)
			call.IsError = true
		}
	}()
	out, err := h(ctx, input)
	if err != nil {
		call.Output = err.Error()
		call.IsError = true
		return call
	}
	if out == "" {
		// Providers reject empty text inside a tool result.
		out = "(no output)"
	}
	call.Output = out
	return call
}

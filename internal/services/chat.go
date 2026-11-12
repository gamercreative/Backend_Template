package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"alphahack/internal/ai"
	"alphahack/internal/queries"
)

const (
	chatDefaultHistoryLimit = 40
	chatMaxRunErrorLen      = 2000
)

type chatService struct {
	store  ChatStore
	client ai.Client
	tools  []ai.Tool
	opts   ChatOptions
}

// NewChatService builds the chat (agent turn) service. See AGENTS.md "Chat flow".
func NewChatService(store ChatStore, client ai.Client, tools []ai.Tool, opts ChatOptions) ChatService {
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = chatDefaultHistoryLimit
	}
	return &chatService{store: store, client: client, tools: tools, opts: opts}
}

func (s *chatService) SendMessage(ctx context.Context, conversationID string, in SendMessageInput) (*ChatResult, error) {
	return s.send(ctx, conversationID, in, nil)
}

// StreamMessage is SendMessage with live events; see the ChatService doc
// comment. A nil emit behaves exactly like SendMessage.
func (s *chatService) StreamMessage(ctx context.Context, conversationID string, in SendMessageInput, emit func(ChatEvent)) (*ChatResult, error) {
	return s.send(ctx, conversationID, in, emit)
}

// send is the shared chat turn. emit may be nil: then nothing is emitted and
// the model client is not asked to stream (ai.Request.OnEvent stays nil).
func (s *chatService) send(ctx context.Context, conversationID string, in SendMessageInput, emit func(ChatEvent)) (*ChatResult, error) {
	// 1. Validate and load.
	content := clean(in.Content)
	if content == "" {
		return nil, Invalid("content is required")
	}
	if utf8.RuneCountInString(content) > MaxContentLen {
		return nil, Invalid("content must be at most %d characters", MaxContentLen)
	}
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, FromStore(err, "conversation")
	}
	agent, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return nil, FromStore(err, "agent")
	}
	model := agent.Model
	if model == "" {
		model = s.opts.DefaultModel
	}

	// 2. Run + user message + touch, atomically.
	var run queries.Run
	var userMsg queries.Message
	err = s.store.InTx(ctx, func(q queries.Querier) error {
		var err error
		run, err = q.CreateRun(ctx, queries.CreateRunParams{ConversationID: conv.ID, Model: model})
		if err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		runID := run.ID
		userMsg, err = q.CreateMessage(ctx, queries.CreateMessageParams{
			ConversationID: conv.ID,
			RunID:          &runID,
			Role:           queries.RoleUser,
			Content:        content,
		})
		if err != nil {
			return fmt.Errorf("create user message: %w", err)
		}
		if err := q.TouchConversation(ctx, conv.ID); err != nil {
			return fmt.Errorf("touch conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, chatPersistErr(err)
	}
	if emit != nil {
		emit(ChatEvent{Type: ChatEventStart, Data: ChatStartData{Run: run, UserMessage: userMsg}})
	}

	// 3. History + model call.
	recent, err := s.store.ListRecentMessages(ctx, conv.ID, s.opts.HistoryLimit)
	if err != nil {
		err = chatPersistErr(fmt.Errorf("list recent messages: %w", err))
		s.failRun(ctx, run, model, nil, err)
		return nil, err
	}
	history := chatBuildHistory(recent, content)

	res, aiErr := s.client.Run(ctx, ai.Request{
		Model:         model,
		System:        agent.SystemPrompt,
		Messages:      history,
		Tools:         s.tools,
		MaxIterations: s.opts.MaxIterations,
		OnEvent:       chatEventAdapter(emit),
	})
	if aiErr != nil {
		// 5. Failure: keep the trace, mark the run failed.
		s.failRun(ctx, run, model, res, aiErr)
		if errors.Is(aiErr, context.DeadlineExceeded) || errors.Is(aiErr, context.Canceled) {
			return nil, aiErr
		}
		return nil, Upstream(aiErr)
	}
	if res == nil {
		res = &ai.Result{}
	}

	// 4. Success: tool calls + assistant message + finish, atomically. The
	// model work is already done, so persist it even if the caller went away.
	text := res.Text
	if text == "" {
		text = fmt.Sprintf("[no text response; stop reason: %s]", res.StopReason)
	}
	finalModel := res.Model
	if finalModel == "" {
		finalModel = model
	}
	pctx := context.WithoutCancel(ctx)
	out := &ChatResult{UserMessage: userMsg, ToolCalls: []queries.ToolCall{}}
	err = s.store.InTx(pctx, func(q queries.Querier) error {
		calls, err := chatStoreToolCalls(pctx, q, run.ID, res.ToolCalls)
		if err != nil {
			return err
		}
		runID := run.ID
		asst, err := q.CreateMessage(pctx, queries.CreateMessageParams{
			ConversationID: conv.ID,
			RunID:          &runID,
			Role:           queries.RoleAssistant,
			Content:        text,
		})
		if err != nil {
			return fmt.Errorf("create assistant message: %w", err)
		}
		finished, err := q.FinishRun(pctx, queries.FinishRunParams{
			ID:           run.ID,
			Status:       queries.RunStatusCompleted,
			StopReason:   res.StopReason,
			Model:        finalModel,
			InputTokens:  res.InputTokens,
			OutputTokens: res.OutputTokens,
			Iterations:   res.Iterations,
		})
		if err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		out.ToolCalls = calls
		out.AssistantMessage = asst
		out.Run = finished
		return nil
	})
	if err != nil {
		// The transaction rolled back; do not leave the run "running".
		s.failRun(ctx, run, model, res, err)
		return nil, Internal(err)
	}
	return out, nil
}

// failRun records a failed run (plus any partial tool calls) in one
// transaction, detached from the request's cancellation. Errors are logged:
// the caller already has a more relevant error to return.
func (s *chatService) failRun(ctx context.Context, run queries.Run, model string, partial *ai.Result, cause error) {
	pctx := context.WithoutCancel(ctx)
	p := queries.FinishRunParams{
		ID:     run.ID,
		Status: queries.RunStatusFailed,
		Error:  chatTruncate(cause.Error(), chatMaxRunErrorLen),
		Model:  model,
	}
	var calls []ai.ToolCall
	if partial != nil {
		calls = partial.ToolCalls
		p.StopReason = partial.StopReason
		p.InputTokens = partial.InputTokens
		p.OutputTokens = partial.OutputTokens
		p.Iterations = partial.Iterations
		if partial.Model != "" {
			p.Model = partial.Model
		}
	}
	err := s.store.InTx(pctx, func(q queries.Querier) error {
		if _, err := chatStoreToolCalls(pctx, q, run.ID, calls); err != nil {
			return err
		}
		if _, err := q.FinishRun(pctx, p); err != nil {
			return fmt.Errorf("finish run: %w", err)
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(pctx, "chat: could not record failed run", "run_id", run.ID, "err", err)
	}
}

func chatStoreToolCalls(ctx context.Context, q queries.Querier, runID string, calls []ai.ToolCall) ([]queries.ToolCall, error) {
	out := make([]queries.ToolCall, 0, len(calls))
	for _, c := range calls {
		input := c.Input
		if len(input) == 0 {
			input = []byte("{}")
		}
		tc, err := q.CreateToolCall(ctx, queries.CreateToolCallParams{
			RunID:      runID,
			Name:       c.Name,
			Input:      input,
			Output:     c.Output,
			IsError:    c.IsError,
			DurationMS: int(c.Duration.Milliseconds()),
		})
		if err != nil {
			return nil, fmt.Errorf("create tool call %q: %w", c.Name, err)
		}
		out = append(out, tc)
	}
	return out, nil
}

// chatBuildHistory converts stored messages (oldest first) into model history
// that starts and ends with a user turn. fallback is the current user message,
// used if nothing usable remains.
func chatBuildHistory(msgs []queries.Message, fallback string) []ai.Message {
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		if clean(m.Content) == "" {
			continue
		}
		var role ai.Role
		switch m.Role {
		case queries.RoleUser:
			role = ai.RoleUser
		case queries.RoleAssistant:
			role = ai.RoleAssistant
		default:
			continue
		}
		if len(out) == 0 && role != ai.RoleUser {
			continue // history must start with a user turn
		}
		out = append(out, ai.Message{Role: role, Content: m.Content})
	}
	for len(out) > 0 && out[len(out)-1].Role != ai.RoleUser {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		out = append(out, ai.Message{Role: ai.RoleUser, Content: fallback})
	}
	return out
}

// chatPersistErr passes context errors through and wraps everything else as Internal.
func chatPersistErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return Internal(err)
}

func chatTruncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func (s *chatService) GetRun(ctx context.Context, id string) (*RunDetail, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return nil, FromStore(err, "run")
	}
	calls, err := s.store.ListToolCalls(ctx, run.ID)
	if err != nil {
		return nil, FromStore(err, "run")
	}
	if calls == nil {
		calls = []queries.ToolCall{}
	}
	return &RunDetail{Run: run, ToolCalls: calls}, nil
}

func (s *chatService) ListRuns(ctx context.Context, conversationID string, p Page) ([]queries.Run, error) {
	if _, err := s.store.GetConversation(ctx, conversationID); err != nil {
		return nil, FromStore(err, "conversation")
	}
	p = p.Normalize()
	runs, err := s.store.ListRuns(ctx, conversationID, p.Limit, p.Offset)
	if err != nil {
		return nil, FromStore(err, "conversation")
	}
	if runs == nil {
		runs = []queries.Run{}
	}
	return runs, nil
}

func (s *chatService) ListTools() []ToolInfo {
	out := make([]ToolInfo, 0, len(s.tools))
	for _, t := range s.tools {
		props := t.Properties
		if props == nil {
			props = map[string]any{}
		}
		req := t.Required
		if req == nil {
			req = []string{}
		}
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: map[string]any{
				"type":       "object",
				"properties": props,
				"required":   req,
			},
		})
	}
	return out
}

// chatEventAdapter maps ai stream events onto ChatEvents. It returns nil for
// a nil emit so the client does not stream. Events without a payload and
// unknown event types are dropped.
func chatEventAdapter(emit func(ChatEvent)) func(ai.Event) {
	if emit == nil {
		return nil
	}
	return func(e ai.Event) {
		switch e.Type {
		case ai.EventTextDelta, ai.EventThinkingDelta:
			if e.Text == "" {
				return
			}
			typ := ChatEventDelta
			if e.Type == ai.EventThinkingDelta {
				typ = ChatEventThinking
			}
			emit(ChatEvent{Type: typ, Data: ChatTextData{Text: e.Text}})
		case ai.EventToolCall:
			if e.Tool == nil {
				return
			}
			emit(ChatEvent{Type: ChatEventToolCall, Data: ChatToolData{Name: e.Tool.Name, Input: e.Tool.Input}})
		case ai.EventToolResult:
			if e.Tool == nil {
				return
			}
			emit(ChatEvent{Type: ChatEventToolResult, Data: ChatToolData{
				Name:       e.Tool.Name,
				Input:      e.Tool.Input,
				Output:     e.Tool.Output,
				IsError:    e.Tool.IsError,
				DurationMS: int(e.Tool.Duration.Milliseconds()),
			}})
		}
	}
}

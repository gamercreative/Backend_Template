package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

const (
	defaultMaxTokens     int64 = 16000
	defaultMaxIterations       = 10
)

// fallbackModels are the models that support server-side refusal fallbacks.
var fallbackModels = map[string]bool{
	"claude-fable-5-2":  true,
	"claude-fable-5-1":  true,
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-sonnet-5-5": true,
}

type anthropicClient struct {
	client anthropic.Client
	opts   Options
}

// NewAnthropic returns a Client backed by the official Anthropic Go SDK.
func NewAnthropic(opts Options) Client {
	return newAnthropic(opts)
}

// newAnthropic allows tests to inject extra SDK options (base URL, retries).
func newAnthropic(opts Options, extra ...option.RequestOption) *anthropicClient {
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaultMaxTokens
	}
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = defaultMaxIterations
	}
	var reqOpts []option.RequestOption
	if opts.APIKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(opts.APIKey))
	}
	reqOpts = append(reqOpts, extra...)
	return &anthropicClient{client: anthropic.NewClient(reqOpts...), opts: opts}
}

func (c *anthropicClient) Run(ctx context.Context, req Request) (*Result, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = c.opts.Model
	}
	if model == "" {
		return nil, errors.New("ai: no model configured")
	}
	maxIter := req.MaxIterations
	if maxIter <= 0 {
		maxIter = c.opts.MaxIterations
	}

	params := anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: c.opts.MaxTokens,
		Messages:  buildHistory(req.Messages),
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}}
	}
	streaming := req.OnEvent != nil
	handlers := toolHandlers(req.Tools)
	for _, t := range req.Tools {
		props := t.Properties
		if props == nil {
			props = map[string]any{}
		}
		tp := anthropic.BetaToolParam{
			Name:        t.Name,
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: props, Required: t.Required},
		}
		if t.Description != "" {
			tp.Description = anthropic.String(t.Description)
		}
		if streaming {
			tp.EagerInputStreaming = anthropic.Bool(true)
		}
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tp})
	}
	if c.opts.Effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(c.opts.Effort)}
	}
	if c.opts.Fallbacks && fallbackModels[model] {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	res := &Result{Model: model}
	partial := func() *Result {
		if res.Iterations == 0 && len(res.ToolCalls) == 0 {
			return nil
		}
		return res
	}

	for {
		if err := ctx.Err(); err != nil {
			return partial(), err
		}
		resp, err := c.call(ctx, req, params)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return partial(), fmt.Errorf("ai: model call %d: %w (%w)", res.Iterations+1, ctxErr, err)
			}
			return partial(), fmt.Errorf("ai: model call %d: %w", res.Iterations+1, err)
		}
		res.Iterations++
		res.InputTokens += resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens
		res.OutputTokens += resp.Usage.OutputTokens
		if resp.Model != "" {
			res.Model = resp.Model
		}
		res.Text = joinText(resp)
		res.StopReason = string(resp.StopReason)
		params.Messages = append(params.Messages, resp.ToParam())

		if resp.StopReason != anthropic.BetaStopReasonToolUse {
			return res, nil
		}
		if res.Iterations >= maxIter {
			// The model still wants tools but the budget is spent; the
			// requested tools are not executed since their results would
			// never be sent back.
			res.StopReason = StopMaxIterations
			return res, nil
		}

		var results []anthropic.BetaContentBlockParamUnion
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.BetaToolUseBlock)
			if !ok {
				continue
			}
			call := execTool(ctx, req, handlers, tu.Name, json.RawMessage(tu.JSON.Input.Raw()))
			res.ToolCalls = append(res.ToolCalls, call)
			results = append(results, anthropic.NewBetaToolResultBlock(tu.ID, call.Output, call.IsError))
		}
		if len(results) == 0 {
			// tool_use stop with no tool_use blocks: nothing to answer, stop here.
			return res, nil
		}
		params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(results...))
	}
}

// call makes one model call. Without OnEvent it is a plain request; with
// OnEvent it streams, emitting text and thinking deltas as they arrive, and
// returns the accumulated message. A stream that fails midway returns an error
// (the partial message is discarded, like a failed non-streaming call).
func (c *anthropicClient) call(ctx context.Context, req Request, params anthropic.BetaMessageNewParams) (*anthropic.BetaMessage, error) {
	if req.OnEvent == nil {
		return c.client.Beta.Messages.New(ctx, params)
	}
	stream := c.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	msg := &anthropic.BetaMessage{}
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("accumulate stream: %w", err)
		}
		if ev.Type == "message_stop" {
			stopped = true
		}
		cbd, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent)
		if !ok {
			continue
		}
		switch d := cbd.Delta.AsAny().(type) {
		case anthropic.BetaTextDelta:
			if d.Text != "" {
				req.emit(Event{Type: EventTextDelta, Text: d.Text})
			}
		case anthropic.BetaThinkingDelta:
			if d.Thinking != "" {
				req.emit(Event{Type: EventThinkingDelta, Text: d.Thinking})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if !stopped {
		// A connection cut mid-stream can end the SSE reader without an
		// error; never treat a truncated message as complete.
		return nil, errors.New("stream ended before message_stop")
	}
	return msg, nil
}

// buildHistory converts plain-text history to SDK messages. Empty turns are
// skipped because the API rejects empty text blocks.
func buildHistory(msgs []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		block := anthropic.NewBetaTextBlock(m.Content)
		if m.Role == RoleAssistant {
			out = append(out, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{
				Content: []anthropic.BetaContentBlockParamUnion{block},
			}})
		} else {
			out = append(out, anthropic.NewBetaUserMessage(block))
		}
	}
	return out
}

func joinText(resp *anthropic.BetaMessage) string {
	var parts []string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok && tb.Text != "" {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

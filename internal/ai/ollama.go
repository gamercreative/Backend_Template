package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	ollamaDefaultBaseURL       = "http://localhost:11434"
	ollamaDefaultMaxIterations = 10
	ollamaMaxErrorBody         = 64 << 10
)

type ollamaClient struct {
	http *http.Client
	opts OllamaOptions
}

// NewOllama returns a Client backed by Ollama's native POST /api/chat endpoint.
// Every model call is streamed (NDJSON); OnEvent only decides whether the
// deltas are reported.
func NewOllama(opts OllamaOptions) Client {
	return newOllama(opts)
}

func newOllama(opts OllamaOptions) *ollamaClient {
	opts.BaseURL = strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if opts.BaseURL == "" {
		opts.BaseURL = ollamaDefaultBaseURL
	}
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = ollamaDefaultMaxIterations
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &ollamaClient{http: hc, opts: opts}
}

// Wire types for /api/chat.

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Think    *bool           `json:"think,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Parameters  ollamaToolParams `json:"parameters"`
}

type ollamaToolParams struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required,omitempty"`
}

type ollamaChunk struct {
	Model   string `json:"model"`
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int64  `json:"prompt_eval_count"`
	EvalCount       int64  `json:"eval_count"`
	Error           string `json:"error"`
}

// ollamaResponse is one fully read model call.
type ollamaResponse struct {
	model        string
	content      string
	toolCalls    []ollamaToolCall
	doneReason   string
	inputTokens  int64
	outputTokens int64
}

func (c *ollamaClient) Run(ctx context.Context, req Request) (*Result, error) {
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

	body := ollamaChatRequest{Model: model, Stream: true}
	if req.System != "" {
		body.Messages = append(body.Messages, ollamaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		body.Messages = append(body.Messages, ollamaMessage{Role: string(m.Role), Content: m.Content})
	}
	for _, t := range req.Tools {
		props := t.Properties
		if props == nil {
			props = map[string]any{}
		}
		body.Tools = append(body.Tools, ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  ollamaToolParams{Type: "object", Properties: props, Required: t.Required},
			},
		})
	}
	switch c.opts.Think {
	case "true":
		v := true
		body.Think = &v
	case "false":
		v := false
		body.Think = &v
	}
	if c.opts.NumCtx > 0 || c.opts.MaxTokens > 0 {
		body.Options = map[string]any{}
		if c.opts.NumCtx > 0 {
			body.Options["num_ctx"] = c.opts.NumCtx
		}
		if c.opts.MaxTokens > 0 {
			body.Options["num_predict"] = c.opts.MaxTokens
		}
	}
	handlers := toolHandlers(req.Tools)

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
		resp, err := c.chat(ctx, req, body)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
				return partial(), fmt.Errorf("ai: model call %d: %w (%w)", res.Iterations+1, ctxErr, err)
			}
			return partial(), fmt.Errorf("ai: model call %d: %w", res.Iterations+1, err)
		}
		res.Iterations++
		res.InputTokens += resp.inputTokens
		res.OutputTokens += resp.outputTokens
		if resp.model != "" {
			res.Model = resp.model
		}
		res.Text = resp.content
		res.StopReason = ollamaStopReason(resp.doneReason)
		body.Messages = append(body.Messages, ollamaMessage{
			Role:      "assistant",
			Content:   resp.content,
			ToolCalls: ollamaHistoryCalls(resp.toolCalls),
		})

		if len(resp.toolCalls) == 0 {
			return res, nil
		}
		if res.Iterations >= maxIter {
			// The model still wants tools but the budget is spent; the
			// requested tools are not executed since their results would
			// never be sent back.
			res.StopReason = StopMaxIterations
			return res, nil
		}
		for _, tc := range resp.toolCalls {
			call := execTool(ctx, req, handlers, tc.Function.Name, tc.Function.Arguments)
			res.ToolCalls = append(res.ToolCalls, call)
			body.Messages = append(body.Messages, ollamaMessage{
				Role:     "tool",
				ToolName: tc.Function.Name,
				Content:  call.Output,
			})
		}
	}
}

// chat makes one streamed /api/chat call, emitting deltas as they arrive.
func (c *ollamaClient) chat(ctx context.Context, req Request, body ollamaChatRequest) (*ollamaResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, fmt.Errorf("cannot reach Ollama at %s (is the server running? start it with `ollama serve`): %w", c.opts.BaseURL, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(httpResp.Body, ollamaMaxErrorBody))
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if msg == "" {
			msg = http.StatusText(httpResp.StatusCode)
		}
		return nil, fmt.Errorf("ollama: HTTP %d: %s", httpResp.StatusCode, msg)
	}

	out := &ollamaResponse{}
	var text strings.Builder
	r := bufio.NewReader(httpResp.Body)
	for {
		line, readErr := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var chunk ollamaChunk
			if err := json.Unmarshal(line, &chunk); err != nil {
				return nil, fmt.Errorf("ollama: decode stream line: %w", err)
			}
			if chunk.Error != "" {
				return nil, fmt.Errorf("ollama: %s", chunk.Error)
			}
			if chunk.Model != "" {
				out.model = chunk.Model
			}
			if s := chunk.Message.Thinking; s != "" {
				req.emit(Event{Type: EventThinkingDelta, Text: s})
			}
			if s := chunk.Message.Content; s != "" {
				text.WriteString(s)
				req.emit(Event{Type: EventTextDelta, Text: s})
			}
			for _, tc := range chunk.Message.ToolCalls {
				tc.Function.Arguments = ollamaArguments(tc.Function.Arguments)
				out.toolCalls = append(out.toolCalls, tc)
			}
			if chunk.Done {
				out.doneReason = chunk.DoneReason
				out.inputTokens = chunk.PromptEvalCount
				out.outputTokens = chunk.EvalCount
				out.content = text.String()
				return out, nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil, errors.New("ollama: stream ended before the done message")
			}
			return nil, fmt.Errorf("ollama: read stream: %w", readErr)
		}
	}
}

// ollamaArguments unquotes tool call arguments that arrive as a JSON-encoded
// string (Ollama normally sends an object). Invalid JSON is passed through so
// execTool reports it to the model as an error result.
func ollamaArguments(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s = strings.TrimSpace(s); s == "" {
				return json.RawMessage("{}")
			}
			return json.RawMessage(s)
		}
	}
	return raw
}

// ollamaHistoryCalls returns the tool calls as they are replayed to Ollama in
// the assistant message: arguments must be a JSON object there, so anything
// else is sent as {}.
func ollamaHistoryCalls(calls []ollamaToolCall) []ollamaToolCall {
	out := make([]ollamaToolCall, len(calls))
	for i, tc := range calls {
		args := tc.Function.Arguments
		if len(args) == 0 || args[0] != '{' || !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		out[i] = ollamaToolCall{Function: ollamaFunctionCall{Name: tc.Function.Name, Arguments: args}}
	}
	return out
}

func ollamaStopReason(r string) string {
	switch r {
	case "stop":
		return StopEndTurn
	case "length":
		return StopMaxTokens
	default:
		return r
	}
}

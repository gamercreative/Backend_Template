package ai

import (
	"context"
	"encoding/json"
	"strings"
)

type fakeClient struct{}

// NewFake returns a deterministic, offline Client. It replies
// "echo: <last user content>", or, for "/tool <name> <json>" naming a tool in
// the request, runs that tool and replies "tool <name> returned: <output>".
func NewFake() Client { return fakeClient{} }

func (fakeClient) Run(ctx context.Context, req Request) (*Result, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content := req.Messages[len(req.Messages)-1].Content
	res := &Result{
		Text:       "echo: " + content,
		StopReason: StopEndTurn,
		Model:      "fake",
		Iterations: 1,
	}

	if rest, ok := strings.CutPrefix(strings.TrimSpace(content), "/tool "); ok {
		name, input, _ := strings.Cut(strings.TrimSpace(rest), " ")
		input = strings.TrimSpace(input)
		if input == "" {
			input = "{}"
		}
		handlers := toolHandlers(req.Tools)
		if _, exists := handlers[name]; exists {
			// execTool rejects invalid JSON input with an error result.
			call := execTool(ctx, req, handlers, name, json.RawMessage(input))
			res.ToolCalls = append(res.ToolCalls, call)
			res.Text = "tool " + name + " returned: " + call.Output
		}
	}
	if req.OnEvent != nil {
		for _, chunk := range splitKeepSpaces(res.Text) {
			req.emit(Event{Type: EventTextDelta, Text: chunk})
		}
	}
	res.InputTokens = int64(len(content))
	res.OutputTokens = int64(len(res.Text))
	return res, nil
}

// splitKeepSpaces splits s after each space so the chunks concatenate back to s.
func splitKeepSpaces(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

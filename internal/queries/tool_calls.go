package queries

import (
	"context"
	"encoding/json"
)

var (
	qCreateToolCall = mustSQL("create_tool_call")
	qListToolCalls  = mustSQL("list_tool_calls")
)

// emptyJSONObject is stored when a tool call has no input.
var emptyJSONObject = []byte("{}")

func scanToolCall(r rowScanner) (ToolCall, error) {
	var tc ToolCall
	var input []byte
	err := r.Scan(&tc.ID, &tc.RunID, &tc.Name, &input, &tc.Output, &tc.IsError, &tc.DurationMS, &tc.CreatedAt)
	tc.Input = json.RawMessage(input)
	return tc, err
}

func (q *Queries) CreateToolCall(ctx context.Context, p CreateToolCallParams) (ToolCall, error) {
	input := []byte(p.Input)
	if len(input) == 0 {
		input = emptyJSONObject
	}
	tc, err := scanToolCall(q.db.QueryRow(ctx, qCreateToolCall,
		p.RunID, p.Name, input, p.Output, p.IsError, p.DurationMS,
	))
	return tc, wrap("create tool call", err)
}

func (q *Queries) ListToolCalls(ctx context.Context, runID string) ([]ToolCall, error) {
	rows, err := q.db.Query(ctx, qListToolCalls, runID)
	if err != nil {
		return nil, wrap("list tool calls", err)
	}
	return collect(rows, "list tool calls", scanToolCall)
}

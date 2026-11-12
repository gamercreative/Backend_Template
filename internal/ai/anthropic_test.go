package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

type captured struct {
	body   map[string]any
	header http.Header
}

// scriptServer replies with the given responses in order. A response that is
// an int is sent as that HTTP error status.
func scriptServer(t *testing.T, responses ...any) (*httptest.Server, *[]captured) {
	t.Helper()
	var mu sync.Mutex
	var reqs []captured
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, captured{body: body, header: r.Header.Clone()})
		i := n
		n++
		mu.Unlock()
		if i >= len(responses) {
			t.Errorf("unexpected request %d", i+1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch v := responses[i].(type) {
		case int:
			w.WriteHeader(v)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
		default:
			_ = json.NewEncoder(w).Encode(v)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func msg(model, stop string, in, out int, content ...map[string]any) map[string]any {
	return map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stop,
		"usage": map[string]any{"input_tokens": in, "output_tokens": out},
	}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func toolUse(id, name string, input any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func testClient(srv *httptest.Server, opts Options) *anthropicClient {
	opts.APIKey = "test-key"
	return newAnthropic(opts, option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
}

func weatherTools(calls *int) []Tool {
	return []Tool{
		{
			Name:        "weather",
			Description: "Get weather",
			Properties:  map[string]any{"city": map[string]any{"type": "string"}},
			Required:    []string{"city"},
			Handler: func(_ context.Context, in json.RawMessage) (string, error) {
				*calls++
				var v struct{ City string }
				_ = json.Unmarshal(in, &v)
				return "sunny in " + v.City, nil
			},
		},
		{
			Name:    "explode",
			Handler: func(context.Context, json.RawMessage) (string, error) { panic("kaboom") },
		},
	}
}

func TestAnthropicToolLoop(t *testing.T) {
	srv, reqs := scriptServer(t,
		msg("claude-opus-5-5", "tool_use", 10, 5,
			text("checking"),
			toolUse("tu_1", "weather", map[string]any{"city": "Paris"}),
			toolUse("tu_2", "explode", map[string]any{}),
			toolUse("tu_3", "missing", map[string]any{}),
		),
		msg("claude-opus-5-5", "end_turn", 20, 7, text("It is"), text("sunny.")),
	)
	var n int
	c := testClient(srv, Options{Model: "claude-opus-5-5", Fallbacks: true, Effort: "high"})
	res, err := c.Run(context.Background(), Request{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}, {Role: RoleUser, Content: "weather?"}},
		Tools:    weatherTools(&n),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "It is\nsunny." || res.StopReason != StopEndTurn || res.Model != "claude-opus-5-5" {
		t.Errorf("result = %+v", res)
	}
	if res.InputTokens != 30 || res.OutputTokens != 12 || res.Iterations != 2 {
		t.Errorf("usage = in %d out %d iter %d", res.InputTokens, res.OutputTokens, res.Iterations)
	}
	if n != 1 || len(res.ToolCalls) != 3 {
		t.Fatalf("handler calls %d, tool calls %d", n, len(res.ToolCalls))
	}
	if tc := res.ToolCalls[0]; tc.IsError || tc.Output != "sunny in Paris" || string(tc.Input) != `{"city":"Paris"}` {
		t.Errorf("call 0 = %+v", tc)
	}
	if !res.ToolCalls[1].IsError || !strings.Contains(res.ToolCalls[1].Output, "kaboom") {
		t.Errorf("panic call = %+v", res.ToolCalls[1])
	}
	if !res.ToolCalls[2].IsError || !strings.Contains(res.ToolCalls[2].Output, "unknown tool") {
		t.Errorf("unknown call = %+v", res.ToolCalls[2])
	}

	if len(*reqs) != 2 {
		t.Fatalf("requests = %d", len(*reqs))
	}
	first := (*reqs)[0].body
	tools := first["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %v", tools)
	}
	w := tools[0].(map[string]any)
	schema := w["input_schema"].(map[string]any)
	if w["name"] != "weather" || w["description"] != "Get weather" || schema["type"] != "object" ||
		schema["required"].([]any)[0] != "city" || schema["properties"].(map[string]any)["city"] == nil {
		t.Errorf("tool param = %v", w)
	}
	sys := first["system"].([]any)[0].(map[string]any)
	if sys["text"] != "be brief" || sys["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("system = %v", sys)
	}
	if first["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("output_config = %v", first["output_config"])
	}
	for _, k := range []string{"thinking", "temperature", "top_p", "top_k", "tool_choice"} {
		if _, ok := first[k]; ok {
			t.Errorf("unexpected %s in request", k)
		}
	}
	if first["fallbacks"] == nil {
		t.Error("fallbacks missing")
	}
	if !strings.Contains((*reqs)[0].header.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("beta header = %q", (*reqs)[0].header.Get("anthropic-beta"))
	}

	second := (*reqs)[1].body["messages"].([]any)
	if len(second) != 5 {
		t.Fatalf("second request messages = %d", len(second))
	}
	last := second[4].(map[string]any)
	results := last["content"].([]any)
	if last["role"] != "user" || len(results) != 3 {
		t.Fatalf("tool result message = %v", last)
	}
	for i, id := range []string{"tu_1", "tu_2", "tu_3"} {
		r := results[i].(map[string]any)
		if r["type"] != "tool_result" || r["tool_use_id"] != id {
			t.Errorf("result %d = %v", i, r)
		}
		if isErr, _ := r["is_error"].(bool); isErr != (i > 0) {
			t.Errorf("result %d is_error = %v", i, r["is_error"])
		}
	}
	if second[3].(map[string]any)["role"] != "assistant" {
		t.Errorf("assistant turn not appended: %v", second[3])
	}
}

func TestAnthropicFallbacksByModel(t *testing.T) {
	tests := []struct {
		model     string
		fallbacks bool
		want      bool
	}{
		{"claude-opus-5-5", true, true},
		{"claude-fable-5-2", true, true},
		{"claude-haiku-5-5", true, false},
		{"claude-opus-5-5", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			srv, reqs := scriptServer(t, msg(tt.model, "end_turn", 1, 1, text("ok")))
			c := testClient(srv, Options{Model: "claude-opus-5", Fallbacks: tt.fallbacks})
			if _, err := c.Run(context.Background(), Request{Model: tt.model, Messages: []Message{{Role: RoleUser, Content: "x"}}}); err != nil {
				t.Fatal(err)
			}
			r := (*reqs)[0]
			_, has := r.body["fallbacks"]
			beta := strings.Contains(r.header.Get("anthropic-beta"), "server-side-fallback")
			if has != tt.want || beta != tt.want {
				t.Errorf("fallbacks=%v beta=%v, want %v", has, beta, tt.want)
			}
			if r.body["model"] != tt.model {
				t.Errorf("model = %v", r.body["model"])
			}
			if _, ok := r.body["system"]; ok {
				t.Error("system should be omitted when empty")
			}
			if _, ok := r.body["output_config"]; ok {
				t.Error("output_config should be omitted without effort")
			}
		})
	}
}

func TestAnthropicMaxIterations(t *testing.T) {
	srv, reqs := scriptServer(t,
		msg("m", "tool_use", 1, 1, toolUse("a", "weather", map[string]any{"city": "A"})),
		msg("m", "tool_use", 1, 1, toolUse("b", "weather", map[string]any{"city": "B"})),
	)
	var n int
	c := testClient(srv, Options{Model: "m", MaxIterations: 5})
	res, err := c.Run(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "loop"}}, Tools: weatherTools(&n), MaxIterations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMaxIterations || res.Iterations != 2 || len(*reqs) != 2 {
		t.Errorf("stop %q iter %d reqs %d", res.StopReason, res.Iterations, len(*reqs))
	}
	if n != 1 || len(res.ToolCalls) != 1 {
		t.Errorf("handler calls %d, tool calls %d", n, len(res.ToolCalls))
	}
}

func TestAnthropicRefusalIsResult(t *testing.T) {
	srv, _ := scriptServer(t, msg("m", "refusal", 3, 0))
	res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil || res.StopReason != StopRefusal {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestAnthropicErrorReturnsPartial(t *testing.T) {
	srv, _ := scriptServer(t,
		msg("m", "tool_use", 4, 2, toolUse("a", "weather", map[string]any{"city": "A"})),
		500,
	)
	var n int
	res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}}, Tools: weatherTools(&n),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if res == nil || res.Iterations != 1 || len(res.ToolCalls) != 1 || res.InputTokens != 4 || res.OutputTokens != 2 {
		t.Fatalf("partial = %+v", res)
	}
}

func TestAnthropicFirstCallErrorNoPartial(t *testing.T) {
	srv, _ := scriptServer(t, 400)
	res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil || res != nil {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestAnthropicContextError(t *testing.T) {
	srv, _ := scriptServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testClient(srv, Options{Model: "m"}).Run(ctx, Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicValidation(t *testing.T) {
	c := newAnthropic(Options{Model: "m"})
	for i, req := range []Request{
		{},
		{Messages: []Message{{Role: RoleUser, Content: "a"}, {Role: RoleAssistant, Content: "b"}}},
	} {
		if _, err := c.Run(context.Background(), req); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

// sseEvent is one server-sent event of a streamed Messages response.
type sseEvent struct {
	name string
	data any
}

// sseBlock describes one content block of a streamed turn and the deltas that fill it.
type sseBlock struct {
	start  map[string]any
	deltas []map[string]any
}

func sseText(chunks ...string) sseBlock {
	b := sseBlock{start: map[string]any{"type": "text", "text": ""}}
	for _, c := range chunks {
		b.deltas = append(b.deltas, map[string]any{"type": "text_delta", "text": c})
	}
	return b
}

func sseThinking(chunks ...string) sseBlock {
	b := sseBlock{start: map[string]any{"type": "thinking", "thinking": "", "signature": ""}}
	for _, c := range chunks {
		b.deltas = append(b.deltas, map[string]any{"type": "thinking_delta", "thinking": c})
	}
	b.deltas = append(b.deltas, map[string]any{"type": "signature_delta", "signature": "sig"})
	return b
}

func sseToolUse(id, name string, jsonChunks ...string) sseBlock {
	b := sseBlock{start: map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}}
	for _, c := range jsonChunks {
		b.deltas = append(b.deltas, map[string]any{"type": "input_json_delta", "partial_json": c})
	}
	return b
}

// sseTurn builds the full event sequence of one streamed model call.
func sseTurn(model, stop string, in, out int, blocks ...sseBlock) []sseEvent {
	ev := []sseEvent{{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": in, "output_tokens": 1},
	}}}}
	for i, b := range blocks {
		ev = append(ev, sseEvent{"content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": b.start}})
		for _, d := range b.deltas {
			ev = append(ev, sseEvent{"content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": d}})
		}
		ev = append(ev, sseEvent{"content_block_stop", map[string]any{"type": "content_block_stop", "index": i}})
	}
	ev = append(ev,
		sseEvent{"message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": out}}},
		sseEvent{"message_stop", map[string]any{"type": "message_stop"}},
	)
	return ev
}

// sseServer answers request i with turns[i] as an SSE stream. A nil turn
// fails the test (unexpected request).
func sseServer(t *testing.T, turns ...[]sseEvent) (*httptest.Server, *[]captured) {
	t.Helper()
	var mu sync.Mutex
	var reqs []captured
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, captured{body: body, header: r.Header.Clone()})
		i := n
		n++
		mu.Unlock()
		if i >= len(turns) {
			t.Errorf("unexpected request %d", i+1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		for _, e := range turns[i] {
			data, _ := json.Marshal(e.data)
			_, _ = io.WriteString(w, "event: "+e.name+"\ndata: "+string(data)+"\n\n")
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// recorder collects events; Run calls OnEvent synchronously, so no locking.
type recorder struct{ events []Event }

func (r *recorder) on(e Event) { r.events = append(r.events, e) }

func (r *recorder) kinds() []string {
	var out []string
	for _, e := range r.events {
		s := string(e.Type)
		switch e.Type {
		case EventTextDelta, EventThinkingDelta:
			s += ":" + e.Text
		default:
			s += ":" + e.Tool.Name
		}
		out = append(out, s)
	}
	return out
}

func TestAnthropicStreamText(t *testing.T) {
	srv, reqs := sseServer(t, sseTurn("claude-opus-5-5", "end_turn", 11, 6,
		sseThinking("hmm", ""), sseText("Hel", "lo ", "world"), sseText("bye")))
	var rec recorder
	res, err := testClient(srv, Options{Model: "claude-opus-5-5"}).Run(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    weatherTools(new(int)),
		OnEvent:  rec.on,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"thinking_delta:hmm", "text_delta:Hel", "text_delta:lo ", "text_delta:world", "text_delta:bye"}
	if got := rec.kinds(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events = %q, want %q", got, want)
	}
	if res.Text != "Hello world\nbye" || res.StopReason != StopEndTurn || res.Model != "claude-opus-5-5" {
		t.Errorf("result = %+v", res)
	}
	if res.InputTokens != 11 || res.OutputTokens != 6 || res.Iterations != 1 {
		t.Errorf("usage = in %d out %d iter %d", res.InputTokens, res.OutputTokens, res.Iterations)
	}
	body := (*reqs)[0].body
	if body["stream"] != true {
		t.Errorf("stream = %v", body["stream"])
	}
	for _, tl := range body["tools"].([]any) {
		if tl.(map[string]any)["eager_input_streaming"] != true {
			t.Errorf("tool without eager_input_streaming: %v", tl)
		}
	}
}

func TestAnthropicStreamToolLoop(t *testing.T) {
	srv, reqs := sseServer(t,
		sseTurn("m", "tool_use", 10, 5,
			sseText("checking"),
			sseToolUse("tu_1", "weather", `{"ci`, `ty": "Pa`, `ris"}`),
			sseToolUse("tu_2", "missing"),
		),
		sseTurn("m", "end_turn", 20, 7, sseText("It is ", "sunny.")),
	)
	var n int
	var rec recorder
	res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "weather?"}},
		Tools:    weatherTools(&n),
		OnEvent:  rec.on,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"text_delta:checking",
		"tool_call:weather", "tool_result:weather",
		"tool_call:missing", "tool_result:missing",
		"text_delta:It is ", "text_delta:sunny.",
	}
	if got := rec.kinds(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events = %q, want %q", got, want)
	}
	var final strings.Builder
	for _, e := range rec.events[5:] {
		final.WriteString(e.Text)
	}
	if final.String() != res.Text || res.Text != "It is sunny." {
		t.Errorf("final deltas %q, text %q", final.String(), res.Text)
	}
	if r := rec.events[2].Tool; r.Output != "sunny in Paris" || r.IsError {
		t.Errorf("tool_result event = %+v", r)
	}
	if res.InputTokens != 30 || res.OutputTokens != 12 || res.Iterations != 2 || res.StopReason != StopEndTurn {
		t.Errorf("result = %+v", res)
	}
	if n != 1 || len(res.ToolCalls) != 2 {
		t.Fatalf("handler calls %d, tool calls %d", n, len(res.ToolCalls))
	}
	if tc := res.ToolCalls[0]; tc.IsError || tc.Output != "sunny in Paris" || !jsonEq(tc.Input, `{"city":"Paris"}`) {
		t.Errorf("call 0 = %+v (input %s)", tc, tc.Input)
	}
	if !res.ToolCalls[1].IsError || string(res.ToolCalls[1].Input) != "{}" {
		t.Errorf("call 1 = %+v", res.ToolCalls[1])
	}
	if len(*reqs) != 2 {
		t.Fatalf("requests = %d", len(*reqs))
	}
	msgs := (*reqs)[1].body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request messages = %d", len(msgs))
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if tu := asst[1].(map[string]any); tu["type"] != "tool_use" || tu["id"] != "tu_1" ||
		tu["input"].(map[string]any)["city"] != "Paris" {
		t.Errorf("replayed tool_use = %v", tu)
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["tool_use_id"] != "tu_1" {
		t.Errorf("tool results = %v", results)
	}
}

func jsonEq(raw json.RawMessage, want string) bool {
	var a, b any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestAnthropicStreamMidStreamFailure(t *testing.T) {
	first := sseTurn("m", "tool_use", 4, 2, sseToolUse("a", "weather", `{"city":"A"}`))
	full := sseTurn("m", "end_turn", 9, 9, sseText("par", "tial"))
	tests := []struct {
		name   string
		second []sseEvent
	}{
		// An error event after some deltas.
		{"error event", append(append([]sseEvent{}, full[:3]...),
			sseEvent{"error", map[string]any{"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "Overloaded"}}})},
		// The connection ends before message_stop.
		{"truncated", full[:4]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := sseServer(t, first, tt.second)
			var n int
			var rec recorder
			res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), Request{
				Messages: []Message{{Role: RoleUser, Content: "x"}}, Tools: weatherTools(&n), OnEvent: rec.on,
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if res == nil || res.Iterations != 1 || len(res.ToolCalls) != 1 || res.InputTokens != 4 || res.OutputTokens != 2 {
				t.Fatalf("partial = %+v", res)
			}
			if got := rec.kinds(); len(got) < 3 || got[2] != "text_delta:par" {
				t.Errorf("events = %q", got)
			}
		})
	}
}

func TestAnthropicNoEventsWithoutOnEvent(t *testing.T) {
	srv, reqs := scriptServer(t,
		msg("m", "tool_use", 1, 1, toolUse("a", "weather", map[string]any{"city": "A"})),
		msg("m", "end_turn", 1, 1, text("done")),
	)
	var n int
	req := Request{Messages: []Message{{Role: RoleUser, Content: "x"}}, Tools: weatherTools(&n)}
	res, err := testClient(srv, Options{Model: "m"}).Run(context.Background(), req)
	if err != nil || res.Text != "done" || len(res.ToolCalls) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	for i, r := range *reqs {
		if _, ok := r.body["stream"]; ok {
			t.Errorf("request %d has stream field", i)
		}
		for _, tl := range r.body["tools"].([]any) {
			if _, ok := tl.(map[string]any)["eager_input_streaming"]; ok {
				t.Errorf("request %d sets eager_input_streaming without OnEvent", i)
			}
		}
	}
}

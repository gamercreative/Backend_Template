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
)

// ollamaReply is one scripted /api/chat response: either an HTTP error
// status with a body, or NDJSON lines streamed with status 200.
type ollamaReply struct {
	status int
	body   string
	lines  []any
	// hook, when set, runs after the lines are written (before the body ends).
	hook func()
}

func ollamaServer(t *testing.T, replies ...ollamaReply) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		i := n
		n++
		mu.Unlock()
		if i >= len(replies) {
			t.Errorf("unexpected request %d", i+1)
			w.WriteHeader(500)
			return
		}
		rep := replies[i]
		if rep.status != 0 && rep.status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rep.status)
			_, _ = io.WriteString(w, rep.body)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, _ := w.(http.Flusher)
		for _, l := range rep.lines {
			var b []byte
			if s, ok := l.(string); ok {
				b = []byte(s)
			} else {
				b, _ = json.Marshal(l)
			}
			_, _ = w.Write(append(b, '\n'))
			if fl != nil {
				fl.Flush()
			}
		}
		if rep.hook != nil {
			rep.hook()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func oChunk(content string) map[string]any {
	return map[string]any{"model": "m1", "message": map[string]any{"role": "assistant", "content": content}, "done": false}
}

func oThink(s string) map[string]any {
	return map[string]any{"model": "m1", "message": map[string]any{"role": "assistant", "content": "", "thinking": s}, "done": false}
}

func oTools(calls ...map[string]any) map[string]any {
	return map[string]any{"model": "m1", "message": map[string]any{"role": "assistant", "content": "", "tool_calls": calls}, "done": false}
}

func oCall(name string, args any) map[string]any {
	return map[string]any{"function": map[string]any{"name": name, "arguments": args}}
}

func oDone(reason string, in, out int) map[string]any {
	return map[string]any{
		"model": "m1", "message": map[string]any{"role": "assistant", "content": ""},
		"done": true, "done_reason": reason, "prompt_eval_count": in, "eval_count": out,
	}
}

func oReq(content string) Request {
	return Request{Messages: []Message{{Role: RoleUser, Content: content}}}
}

type eventLog struct{ events []Event }

func (l *eventLog) on(e Event) { l.events = append(l.events, e) }

func (l *eventLog) texts(typ EventType) []string {
	var out []string
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e.Text)
		}
	}
	return out
}

func oTools1(calls *int) []Tool {
	return []Tool{{
		Name:        "weather",
		Description: "Get the weather",
		Properties:  map[string]any{"city": map[string]any{"type": "string"}},
		Required:    []string{"city"},
		Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
			*calls++
			var in struct{ City string }
			if err := json.Unmarshal(input, &in); err != nil {
				return "", err
			}
			return "sunny in " + in.City, nil
		},
	}}
}

func TestOllamaPlainText(t *testing.T) {
	srv, bodies := ollamaServer(t, ollamaReply{lines: []any{oChunk("Hel"), oChunk("lo"), oChunk(" there"), oDone("stop", 12, 3)}})
	c := NewOllama(OllamaOptions{BaseURL: srv.URL + "/", Model: "m1"})
	var log eventLog
	req := Request{
		System: "be nice",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "  "},
			{Role: RoleAssistant, Content: "yo"},
			{Role: RoleUser, Content: "again"},
		},
		OnEvent: log.on,
	}
	res, err := c.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello there" || res.StopReason != StopEndTurn || res.Model != "m1" ||
		res.InputTokens != 12 || res.OutputTokens != 3 || res.Iterations != 1 || len(res.ToolCalls) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.Join(log.texts(EventTextDelta), "|"); got != "Hel|lo| there" {
		t.Fatalf("deltas = %q", got)
	}
	b := (*bodies)[0]
	if b["model"] != "m1" || b["stream"] != true {
		t.Fatalf("body = %v", b)
	}
	msgs := b["messages"].([]any)
	wantRoles := []string{"system", "user", "assistant", "user"}
	if len(msgs) != len(wantRoles) {
		t.Fatalf("messages = %v", msgs)
	}
	for i, r := range wantRoles {
		if msgs[i].(map[string]any)["role"] != r {
			t.Fatalf("message %d = %v", i, msgs[i])
		}
	}
	if msgs[0].(map[string]any)["content"] != "be nice" {
		t.Fatalf("system = %v", msgs[0])
	}
	for _, k := range []string{"tools", "think", "options"} {
		if _, ok := b[k]; ok {
			t.Fatalf("%s should be omitted: %v", k, b)
		}
	}

	// Same stream without OnEvent gives the same result.
	srv2, _ := ollamaServer(t, ollamaReply{lines: []any{oChunk("Hel"), oChunk("lo"), oDone("length", 1, 2)}})
	res, err = NewOllama(OllamaOptions{BaseURL: srv2.URL, Model: "m1"}).Run(context.Background(), oReq("hi"))
	if err != nil || res.Text != "Hello" || res.StopReason != StopMaxTokens {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestOllamaThinking(t *testing.T) {
	srv, _ := ollamaServer(t, ollamaReply{lines: []any{oThink("let me "), oThink("think"), oChunk("42"), oDone("stop", 1, 1)}})
	var log eventLog
	req := oReq("q")
	req.OnEvent = log.on
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "42" {
		t.Fatalf("text = %q", res.Text)
	}
	if got := strings.Join(log.texts(EventThinkingDelta), "|"); got != "let me |think" {
		t.Fatalf("thinking = %q", got)
	}
	if len(log.events) != 3 || log.events[2].Type != EventTextDelta {
		t.Fatalf("events = %+v", log.events)
	}
}

func TestOllamaToolLoop(t *testing.T) {
	srv, bodies := ollamaServer(t,
		ollamaReply{lines: []any{oChunk("Checking."), oTools(oCall("weather", map[string]any{"city": "Paris"})), oDone("stop", 10, 5)}},
		ollamaReply{lines: []any{oChunk("It is sunny."), oDone("stop", 30, 7)}},
	)
	calls := 0
	var log eventLog
	req := oReq("weather in Paris?")
	req.Tools = oTools1(&calls)
	req.OnEvent = log.on
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || res.Iterations != 2 || res.InputTokens != 40 || res.OutputTokens != 12 ||
		res.Text != "It is sunny." || res.StopReason != StopEndTurn {
		t.Fatalf("result = %+v calls = %d", res, calls)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Output != "sunny in Paris" || res.ToolCalls[0].IsError ||
		string(res.ToolCalls[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}

	var types []EventType
	for _, e := range log.events {
		types = append(types, e.Type)
	}
	want := []EventType{EventTextDelta, EventToolCall, EventToolResult, EventTextDelta}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v", types)
		}
	}

	if len(*bodies) != 2 {
		t.Fatalf("requests = %d", len(*bodies))
	}
	tools := (*bodies)[0]["tools"].([]any)
	fn := tools[0].(map[string]any)
	if fn["type"] != "function" {
		t.Fatalf("tool = %v", fn)
	}
	f := fn["function"].(map[string]any)
	params := f["parameters"].(map[string]any)
	if f["name"] != "weather" || f["description"] != "Get the weather" || params["type"] != "object" ||
		params["properties"].(map[string]any)["city"] == nil || params["required"].([]any)[0] != "city" {
		t.Fatalf("tool = %v", fn)
	}
	if n := len((*bodies)[0]["messages"].([]any)); n != 1 {
		t.Fatalf("first call messages = %d", n)
	}
	msgs := (*bodies)[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second call messages = %v", msgs)
	}
	asst := msgs[1].(map[string]any)
	tcs := asst["tool_calls"].([]any)
	args := tcs[0].(map[string]any)["function"].(map[string]any)["arguments"].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != "Checking." || args["city"] != "Paris" {
		t.Fatalf("assistant = %v", asst)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_name"] != "weather" || tool["content"] != "sunny in Paris" {
		t.Fatalf("tool message = %v", tool)
	}
}

func TestOllamaStringArguments(t *testing.T) {
	srv, bodies := ollamaServer(t,
		ollamaReply{lines: []any{oTools(oCall("weather", `{"city":"Rome"}`), oCall("weather", `not json`)), oDone("stop", 1, 1)}},
		ollamaReply{lines: []any{oChunk("done"), oDone("stop", 1, 1)}},
	)
	calls := 0
	req := oReq("q")
	req.Tools = oTools1(&calls)
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(res.ToolCalls) != 2 {
		t.Fatalf("calls = %d tool calls = %+v", calls, res.ToolCalls)
	}
	if res.ToolCalls[0].Output != "sunny in Rome" || string(res.ToolCalls[0].Input) != `{"city":"Rome"}` {
		t.Fatalf("first = %+v", res.ToolCalls[0])
	}
	if !res.ToolCalls[1].IsError {
		t.Fatalf("second should be an error: %+v", res.ToolCalls[1])
	}
	// Replayed arguments are always JSON objects.
	tcs := (*bodies)[1]["messages"].([]any)[1].(map[string]any)["tool_calls"].([]any)
	for i, tc := range tcs {
		if _, ok := tc.(map[string]any)["function"].(map[string]any)["arguments"].(map[string]any); !ok {
			t.Fatalf("tool call %d arguments not an object: %v", i, tc)
		}
	}
}

func TestOllamaMaxIterations(t *testing.T) {
	turn := ollamaReply{lines: []any{oTools(oCall("weather", map[string]any{"city": "X"})), oDone("stop", 2, 3)}}
	srv, bodies := ollamaServer(t, turn, turn)
	calls := 0
	req := oReq("q")
	req.Tools = oTools1(&calls)
	req.MaxIterations = 2
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1", MaxIterations: 9}).Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMaxIterations || res.Iterations != 2 || calls != 1 || len(res.ToolCalls) != 1 ||
		res.InputTokens != 4 || res.OutputTokens != 6 || len(*bodies) != 2 {
		t.Fatalf("result = %+v calls = %d", res, calls)
	}
}

func TestOllamaStreamErrorLine(t *testing.T) {
	srv, _ := ollamaServer(t, ollamaReply{lines: []any{oChunk("partial"), `{"error":"model runner crashed"}`}})
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), oReq("q"))
	if err == nil || !strings.Contains(err.Error(), "model runner crashed") {
		t.Fatalf("err = %v", err)
	}
	if res != nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestOllamaStreamTruncated(t *testing.T) {
	srv, _ := ollamaServer(t, ollamaReply{lines: []any{oChunk("partial")}})
	_, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), oReq("q"))
	if err == nil || !strings.Contains(err.Error(), "done") {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaHTTPError(t *testing.T) {
	srv, _ := ollamaServer(t, ollamaReply{status: 404, body: `{"error":"model \"nope\" not found, try pulling it first"}`})
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "nope"}).Run(context.Background(), oReq("q"))
	if err == nil || !strings.Contains(err.Error(), `model "nope" not found`) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	if res != nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestOllamaConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := NewOllama(OllamaOptions{BaseURL: url, Model: "m1"}).Run(context.Background(), oReq("q"))
	if err == nil || !strings.Contains(err.Error(), "ollama serve") || !strings.Contains(err.Error(), url) {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv, bodies := ollamaServer(t)
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(ctx, oReq("q"))
	if !errors.Is(err, context.Canceled) || res != nil || len(*bodies) != 0 {
		t.Fatalf("res = %+v err = %v", res, err)
	}

	// Cancelled mid-stream: the error still matches context.Canceled.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	release := make(chan struct{})
	srv2, _ := ollamaServer(t, ollamaReply{lines: []any{oChunk("a")}, hook: func() {
		cancel2()
		<-release
	}})
	defer close(release)
	_, err = NewOllama(OllamaOptions{BaseURL: srv2.URL, Model: "m1"}).Run(ctx2, oReq("q"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaPartialResultOnSecondCallFailure(t *testing.T) {
	srv, _ := ollamaServer(t,
		ollamaReply{lines: []any{oTools(oCall("weather", map[string]any{"city": "Oslo"})), oDone("stop", 5, 6)}},
		ollamaReply{status: 500, body: `{"error":"out of memory"}`},
	)
	calls := 0
	req := oReq("q")
	req.Tools = oTools1(&calls)
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "out of memory") || !strings.Contains(err.Error(), "model call 2") {
		t.Fatalf("err = %v", err)
	}
	if res == nil || res.Iterations != 1 || len(res.ToolCalls) != 1 || res.InputTokens != 5 || res.OutputTokens != 6 {
		t.Fatalf("partial = %+v", res)
	}
}

func TestOllamaThinkAndOptions(t *testing.T) {
	tests := []struct {
		name      string
		opts      OllamaOptions
		think     any // nil = absent
		numCtx    any
		numPred   any
		noOptions bool
	}{
		{name: "absent", opts: OllamaOptions{}, noOptions: true},
		{name: "think true", opts: OllamaOptions{Think: "true", NumCtx: 8192}, think: true, numCtx: 8192.0},
		{name: "think false", opts: OllamaOptions{Think: "false", MaxTokens: 256}, think: false, numPred: 256.0},
		{name: "both options", opts: OllamaOptions{NumCtx: 4096, MaxTokens: 100}, numCtx: 4096.0, numPred: 100.0},
		{name: "bogus think", opts: OllamaOptions{Think: "maybe"}, noOptions: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, bodies := ollamaServer(t, ollamaReply{lines: []any{oChunk("ok"), oDone("stop", 1, 1)}})
			tt.opts.BaseURL = srv.URL
			tt.opts.Model = "m1"
			if _, err := NewOllama(tt.opts).Run(context.Background(), oReq("q")); err != nil {
				t.Fatal(err)
			}
			b := (*bodies)[0]
			if think, ok := b["think"]; (tt.think == nil) == ok || (ok && think != tt.think) {
				t.Fatalf("think = %v (present %v), want %v", think, ok, tt.think)
			}
			opts, ok := b["options"].(map[string]any)
			if tt.noOptions {
				if _, present := b["options"]; present {
					t.Fatalf("options should be absent: %v", b["options"])
				}
				return
			}
			if !ok {
				t.Fatalf("options missing: %v", b)
			}
			if v, ok := opts["num_ctx"]; (tt.numCtx == nil) == ok || (ok && v != tt.numCtx) {
				t.Fatalf("num_ctx = %v", opts)
			}
			if v, ok := opts["num_predict"]; (tt.numPred == nil) == ok || (ok && v != tt.numPred) {
				t.Fatalf("num_predict = %v", opts)
			}
		})
	}
}

func TestOllamaValidation(t *testing.T) {
	c := NewOllama(OllamaOptions{})
	if _, err := c.Run(context.Background(), Request{}); err == nil {
		t.Fatal("want error for empty request")
	}
	if _, err := c.Run(context.Background(), oReq("q")); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaLongLine(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	srv, _ := ollamaServer(t, ollamaReply{lines: []any{oChunk(long), oDone("stop", 1, 1)}})
	res, err := NewOllama(OllamaOptions{BaseURL: srv.URL, Model: "m1"}).Run(context.Background(), oReq("q"))
	if err != nil || len(res.Text) != len(long) {
		t.Fatalf("err = %v", err)
	}
}

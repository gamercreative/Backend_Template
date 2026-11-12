package httpx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSSEHeadersAndStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	NewSSE(rec)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if !rec.Flushed {
		t.Error("NewSSE did not flush")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body after NewSSE = %q, want empty", rec.Body.String())
	}
}

func TestSSEExactBytes(t *testing.T) {
	t.Run("one event", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s := NewSSE(rec)
		if err := s.Send("message", map[string]any{"id": 1, "text": "hi"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		want := "event: message\ndata: {\"id\":1,\"text\":\"hi\"}\n\n"
		if got := rec.Body.String(); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("several events in sequence", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s := NewSSE(rec)
		if err := s.Send("start", "go"); err != nil {
			t.Fatalf("Send start: %v", err)
		}
		if err := s.Send("delta", map[string]string{"t": "a"}); err != nil {
			t.Fatalf("Send delta: %v", err)
		}
		if err := s.Send("done", nil); err != nil {
			t.Fatalf("Send done: %v", err)
		}
		want := "event: start\ndata: \"go\"\n\n" +
			"event: delta\ndata: {\"t\":\"a\"}\n\n" +
			"event: done\ndata: null\n\n"
		if got := rec.Body.String(); got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
}

// sseReadEvent reads one complete frame (up to the blank line) from r.
func sseReadEvent(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err != nil {
			return b.String(), err
		}
		if line == "\n" {
			return b.String(), nil
		}
	}
}

// TestSSEFlushesEachEvent checks that every event reaches a real client while
// the handler is still running: the handler blocks until the client has read
// the first event.
func TestSSEFlushesEachEvent(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := NewSSE(w)
		if err := s.Send("first", 1); err != nil {
			t.Errorf("Send first: %v", err)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if err := s.Send("second", 2); err != nil {
			t.Errorf("Send second: %v", err)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	first, err := sseReadEvent(br)
	if err != nil {
		t.Fatalf("reading first event before handler continues: %v", err)
	}
	if want := "event: first\ndata: 1\n\n"; first != want {
		t.Errorf("first = %q, want %q", first, want)
	}

	close(release)
	second, err := sseReadEvent(br)
	if err != nil {
		t.Fatalf("reading second event: %v", err)
	}
	if want := "event: second\ndata: 2\n\n"; second != want {
		t.Errorf("second = %q, want %q", second, want)
	}
}

// syncBuffer is a log sink safe for the server goroutine to write and the test to read.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSSEThroughMiddleware(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	release := make(chan struct{})
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := NewSSE(w)
		_ = s.Send("first", "a")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = s.Send("second", "b")
	}), WithRequestID(), WithLogger(logger), WithRecover(logger))

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	first, err := sseReadEvent(br)
	if err != nil {
		t.Fatalf("first event through middleware: %v", err)
	}
	if want := "event: first\ndata: \"a\"\n\n"; first != want {
		t.Errorf("first = %q, want %q", first, want)
	}
	close(release)
	second, err := sseReadEvent(br)
	if err != nil {
		t.Fatalf("second event through middleware: %v", err)
	}
	if want := "event: second\ndata: \"b\"\n\n"; second != want {
		t.Errorf("second = %q, want %q", second, want)
	}
	// Reading to EOF means the handler has returned, so the logger has run.
	_, _ = io.Copy(io.Discard, resp.Body)

	if got := logs.String(); !strings.Contains(got, "status=200") {
		t.Errorf("logger did not record status 200: %s", got)
	}
}

func TestSSEMarshalFailureWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	s := NewSSE(rec)
	err := s.Send("bad", make(chan int))
	if err == nil {
		t.Fatal("Send of a channel returned nil error")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	// The stream is still usable afterwards.
	if err := s.Send("ok", 1); err != nil {
		t.Fatalf("Send after failed marshal: %v", err)
	}
	if want := "event: ok\ndata: 1\n\n"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestSSEInvalidEventNames(t *testing.T) {
	tests := []struct {
		name  string
		event string
	}{
		{"empty", ""},
		{"lf", "a\nb"},
		{"cr", "a\rb"},
		{"crlf", "a\r\nb"},
		{"trailing lf", "msg\n"},
		{"leading cr", "\rmsg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s := NewSSE(rec)
			if err := s.Send(tc.event, 1); err == nil {
				t.Fatalf("Send(%q) returned nil error", tc.event)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
		})
	}
}

type sseFailWriter struct {
	*httptest.ResponseRecorder
	fail bool
}

func (w *sseFailWriter) Write(b []byte) (int, error) {
	if w.fail {
		return 0, errors.New("client gone")
	}
	return w.ResponseRecorder.Write(b)
}

func TestSSEWriteErrorIsReturned(t *testing.T) {
	w := &sseFailWriter{ResponseRecorder: httptest.NewRecorder()}
	s := NewSSE(w)
	w.fail = true
	if err := s.Send("x", 1); err == nil {
		t.Fatal("Send returned nil error after a failed write")
	}
}

func TestSSERoundTripJSON(t *testing.T) {
	type payload struct {
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}
	in := payload{
		Text: "line one\nline two\r\n<b>bold</b> & \"quoted\"",
		Tags: []string{"<", ">", "&", " "},
	}
	rec := httptest.NewRecorder()
	s := NewSSE(rec)
	if err := s.Send("payload", in); err != nil {
		t.Fatalf("Send: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("frame has %d lines, want 2: %q", len(lines), rec.Body.String())
	}
	if lines[0] != "event: payload" {
		t.Errorf("event line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("data line = %q", lines[1])
	}
	var out payload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &out); err != nil {
		t.Fatalf("decode data line: %v", err)
	}
	if out.Text != in.Text {
		t.Errorf("Text = %q, want %q", out.Text, in.Text)
	}
	if len(out.Tags) != len(in.Tags) {
		t.Fatalf("Tags = %q, want %q", out.Tags, in.Tags)
	}
	for i := range in.Tags {
		if out.Tags[i] != in.Tags[i] {
			t.Errorf("Tags[%d] = %q, want %q", i, out.Tags[i], in.Tags[i])
		}
	}
}

package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// SSEWriter writes a server-sent event stream.
//
// Contract (owned by the orchestrator, signatures frozen; the SSE worker
// replaces the panicking bodies and may add unexported fields).
//
// An SSEWriter is not safe for concurrent use: Send must be called from one
// goroutine only. No locking is done.
type SSEWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// NewSSE starts an event stream on w: it sets Content-Type text/event-stream,
// Cache-Control no-cache, Connection keep-alive and X-Accel-Buffering no,
// writes status 200 and flushes. Nothing else may have been written to w.
func NewSSE(w http.ResponseWriter) *SSEWriter {
	s := &SSEWriter{w: w, rc: http.NewResponseController(w)}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// A writer without flush support just buffers; that is not an error here.
	_ = s.flush()
	return s
}

// Send writes one event, "event: <event>\ndata: <json of v>\n\n", and flushes
// (through http.ResponseController, so it works behind wrapping middleware).
// It returns an error when v cannot be marshalled or the client is gone.
func (s *SSEWriter) Send(event string, v any) error {
	if event == "" {
		return errors.New("httpx: sse event name is empty")
	}
	if strings.ContainsAny(event, "\r\n") {
		return fmt.Errorf("httpx: sse event name %q contains a line break", event)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("httpx: sse marshal %q: %w", event, err)
	}
	// encoding/json never emits raw newlines, so one data line is enough.
	frame := make([]byte, 0, len(event)+len(data)+len("event: \ndata: \n\n"))
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, data...)
	frame = append(frame, "\n\n"...)
	if _, err := s.w.Write(frame); err != nil {
		return err
	}
	return s.flush()
}

// flush pushes buffered bytes to the client. A writer that cannot flush is
// not an error; any other failure (typically a client that has gone) is.
func (s *SSEWriter) flush() error {
	err := s.rc.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

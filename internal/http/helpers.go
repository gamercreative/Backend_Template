package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"runtime/debug"
)

const (
	headerRequestID = "X-Request-ID"
	corsMethods     = "GET, POST, PATCH, DELETE, OPTIONS"
	corsHeaders     = "Content-Type, Authorization, X-Request-ID"
	maxRequestIDLen = 64
)

// validRequestID reports whether id is 1-64 characters of [A-Za-z0-9._-].
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// newRequestID returns 16 random bytes as lowercase hex.
func newRequestID() string {
	var b [16]byte
	// crypto/rand.Read does not fail on supported platforms (Go 1.24+ crashes
	// the program instead of returning an error), so the error is ignored.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func debugStack() []byte {
	return debug.Stack()
}

// respWriter wraps a ResponseWriter to remember the status code, the number of
// body bytes and whether anything has been written yet.
type respWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func newRespWriter(w http.ResponseWriter) *respWriter {
	return &respWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *respWriter) WriteHeader(code int) {
	// Informational responses (1xx, except 101) do not finish the response.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if !w.wrote {
		w.wrote = true
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *respWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing.
func (w *respWriter) Flush() {
	if !w.wrote {
		w.wrote = true
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *respWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

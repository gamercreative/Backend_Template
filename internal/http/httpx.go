// Package httpx (directory internal/http) holds the HTTP plumbing shared by all
// handlers: the single JSON error format, JSON helpers, middleware, the router
// wrapper and the server constructor. It knows nothing about the domain.
//
// Contract (owned by the orchestrator): every exported name and signature in
// this file is frozen. The http worker replaces the panicking bodies and may
// split the code across more files in this package.
//
// The ONE error format, used for every non-2xx response the server produces
// (handler errors, 404, 405, bad JSON, panics, timeouts):
//
//	{"error": {"code": "not_found", "message": "agent not found", "request_id": "..."}}
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Error codes produced by this package. Domain codes come from internal/services.
const (
	CodeBadRequest       = "bad_request"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeTimeout          = "timeout"
	CodeInternal         = "internal_error"
)

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// requestIDKey is the context key under which WithRequestID stores the id.
type requestIDKey struct{}

// JSON writes v as JSON with the given status and Content-Type application/json.
// A nil v writes the status with no body (use for 204).
func JSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; an encode failure can only mean the client
	// went away, so there is nothing useful to do with it.
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the standard error body. request_id comes from r's context.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var id string
	if r != nil {
		id = RequestID(r.Context())
	}
	JSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message, RequestID: id}})
}

// DecodeJSON reads a single JSON object from the request body into dst.
// It caps the body at 1 MiB, rejects unknown fields, empty bodies and trailing
// data. The returned error's message is safe to show to the client.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeJSON(w, r, dst)
}

// RequestID returns the request id stored by WithRequestID, or "".
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first one listed is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			h = mws[i](h)
		}
	}
	return h
}

// WithRequestID reuses an incoming X-Request-ID header (if sane) or generates
// one, stores it in the context and echoes it in the X-Request-ID response header.
func WithRequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(headerRequestID)
			if !validRequestID(id) {
				id = newRequestID()
			}
			w.Header().Set(headerRequestID, id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithLogger logs one line per request: method, path, status, bytes, duration, request_id.
func WithLogger(l *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := newRespWriter(w)
			next.ServeHTTP(rw, r)
			logger := l
			if logger == nil {
				logger = slog.Default()
			}
			level := slog.LevelInfo
			if rw.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Int("bytes", rw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestID(r.Context())),
			)
		})
	}
}

// WithRecover turns a panic into a 500 in the standard error format and logs the stack.
func WithRecover(l *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := newRespWriter(w)
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				// http.ErrAbortHandler is the sanctioned way to abort a response
				// silently; net/http handles it, so it must not be swallowed.
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger := l
				if logger == nil {
					logger = slog.Default()
				}
				logger.Error("panic serving request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("request_id", RequestID(r.Context())),
					slog.Any("panic", v),
					slog.String("stack", string(debugStack())),
				)
				if !rw.wrote {
					Error(rw, r, http.StatusInternalServerError, CodeInternal, "internal error")
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// WithCORS handles preflight and sets CORS headers. origins may contain "*".
func WithCORS(origins []string) Middleware {
	allowAll := false
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		switch o {
		case "":
		case "*":
			allowAll = true
		default:
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			origin := r.Header.Get("Origin")
			switch {
			case allowAll:
				h.Set("Access-Control-Allow-Origin", "*")
			case origin != "" && allowed[origin]:
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
			default:
				h.Add("Vary", "Origin")
			}
			if h.Get("Access-Control-Allow-Origin") != "" {
				h.Set("Access-Control-Expose-Headers", headerRequestID)
			}
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", corsMethods)
				h.Set("Access-Control-Allow-Headers", corsHeaders)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WithTimeout puts a deadline on the request context. It does not write a
// response itself; handlers map context.DeadlineExceeded to 504 CodeTimeout.
func WithTimeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NewRouter wraps a Go 1.22 ServeMux so that unmatched routes (404) and wrong
// methods (405, keeping the Allow header) answer in the standard error format
// instead of the mux's plain-text defaults.
func NewRouter(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		probe := &discardWriter{header: make(http.Header)}
		h.ServeHTTP(probe, r)
		if probe.status == http.StatusMethodNotAllowed {
			if allow := probe.header.Get("Allow"); allow != "" {
				w.Header().Set("Allow", allow)
			}
			Error(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
			return
		}
		Error(w, r, http.StatusNotFound, CodeNotFound, "route not found")
	})
}

// NewServer returns an *http.Server with sane header/idle timeouts. It sets no
// WriteTimeout because agent runs are long; WithTimeout bounds them instead.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

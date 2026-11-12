package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// reqWithID returns r with the request id stored in its context, as WithRequestID would.
func reqWithID(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
}

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRequestID(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		name     string
		incoming string
		set      bool
		wantEcho bool // true: the incoming id must be reused
	}{
		{name: "absent generates", set: false},
		{name: "valid echoes", incoming: "abc-123_x.y", set: true, wantEcho: true},
		{name: "64 chars echoes", incoming: long, set: true, wantEcho: true},
		{name: "65 chars rejected", incoming: long + "a", set: true},
		{name: "space rejected", incoming: "a b", set: true},
		{name: "slash rejected", incoming: "a/b", set: true},
		{name: "unicode rejected", incoming: "é", set: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = RequestID(r.Context())
			}), WithRequestID())
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.set {
				req.Header.Set("X-Request-ID", tc.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			echoed := rec.Header().Get("X-Request-ID")
			if echoed != seen {
				t.Fatalf("header %q != context %q", echoed, seen)
			}
			switch {
			case tc.wantEcho && seen != tc.incoming:
				t.Fatalf("id = %q, want incoming %q", seen, tc.incoming)
			case !tc.wantEcho && !hexID.MatchString(seen):
				t.Fatalf("generated id = %q, want 32 hex chars", seen)
			}
		})
	}
}

func TestRequestIDGeneratedIsUnique(t *testing.T) {
	a, b := newRequestID(), newRequestID()
	if a == b {
		t.Fatalf("two generated ids are equal: %q", a)
	}
}

func TestRecoverBeforeWriteGives500JSON(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("secret database password")
	}), WithRequestID(), WithRecover(logger))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "rid-7")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("panic text leaked to client: %q", rec.Body.String())
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if body.Error.Code != CodeInternal || body.Error.Message != "internal error" || body.Error.RequestID != "rid-7" {
		t.Fatalf("body = %+v", body.Error)
	}
	if !strings.Contains(logs.String(), "secret database password") {
		t.Fatalf("panic value not logged: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "stack=") || !strings.Contains(logs.String(), "goroutine") {
		t.Fatalf("stack not logged: %q", logs.String())
	}
}

func TestRecoverAfterWriteKeepsResponse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	}), WithRecover(logger))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want the already-written 202", rec.Code)
	}
}

func TestRecoverPassesThroughNormalRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}), WithRecover(logger))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	tests := []struct {
		name       string
		origins    []string
		method     string
		origin     string
		wantACAO   string
		wantVary   bool
		wantStatus int
	}{
		{name: "wildcard simple", origins: []string{"*"}, method: http.MethodGet, origin: "https://a.example", wantACAO: "*", wantStatus: 200},
		{name: "wildcard preflight", origins: []string{"*"}, method: http.MethodOptions, origin: "https://a.example", wantACAO: "*", wantStatus: 204},
		{name: "exact match echoes", origins: []string{"https://a.example", "https://b.example"}, method: http.MethodGet, origin: "https://b.example", wantACAO: "https://b.example", wantVary: true, wantStatus: 200},
		{name: "no match no ACAO", origins: []string{"https://a.example"}, method: http.MethodGet, origin: "https://evil.example", wantVary: true, wantStatus: 200},
		{name: "prefix is not a match", origins: []string{"https://a.example"}, method: http.MethodGet, origin: "https://a.example.evil", wantVary: true, wantStatus: 200},
		{name: "no origin header", origins: []string{"https://a.example"}, method: http.MethodGet, origin: "", wantVary: true, wantStatus: 200},
		{name: "preflight allowed origin", origins: []string{"https://a.example"}, method: http.MethodOptions, origin: "https://a.example", wantACAO: "https://a.example", wantVary: true, wantStatus: 204},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Chain(ok, WithCORS(tc.origins))
			req := httptest.NewRequest(tc.method, "/", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantACAO {
				t.Fatalf("ACAO = %q, want %q", got, tc.wantACAO)
			}
			hasVary := false
			for _, v := range rec.Header().Values("Vary") {
				if v == "Origin" {
					hasVary = true
				}
			}
			if hasVary != tc.wantVary {
				t.Fatalf("Vary: Origin present = %v, want %v", hasVary, tc.wantVary)
			}
			if tc.method == http.MethodOptions {
				if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PATCH, DELETE, OPTIONS" {
					t.Fatalf("Allow-Methods = %q", got)
				}
				if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, X-Request-ID" {
					t.Fatalf("Allow-Headers = %q", got)
				}
			}
		})
	}
}

func TestCORSPreflightDoesNotCallNext(t *testing.T) {
	called := false
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}), WithCORS([]string{"*"}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodOptions, "/", nil))
	if called {
		t.Fatal("preflight reached the handler")
	}
}

func TestCORSExposesRequestID(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		WithRequestID(), WithCORS([]string{"https://a.example"}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://a.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-ID" {
		t.Fatalf("Expose-Headers = %q", got)
	}
}

func TestWithTimeoutSetsDeadline(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, hasDeadline = r.Context().Deadline()
	}), WithTimeout(2*time.Second))

	before := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !hasDeadline {
		t.Fatal("no deadline on request context")
	}
	// The deadline is set a little after `before`, so allow generous slack.
	if d := deadline.Sub(before); d < 1500*time.Millisecond || d > 2500*time.Millisecond {
		t.Fatalf("deadline offset = %v, want about 2s", d)
	}
}

func TestWithTimeoutCancelsContext(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			if r.Context().Err() != context.DeadlineExceeded {
				t.Errorf("err = %v", r.Context().Err())
			}
		case <-time.After(2 * time.Second):
			t.Error("context never expired")
		}
	}), WithTimeout(10*time.Millisecond))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

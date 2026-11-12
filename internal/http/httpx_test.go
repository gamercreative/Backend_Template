package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) ErrorBody {
	t.Helper()
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error format: %v; body=%q", err, rec.Body.String())
	}
	return body
}

func TestErrorBodyShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Error(rec, reqWithID(req, "req-123"), http.StatusNotFound, CodeNotFound, "agent not found")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	want := `{"error":{"code":"not_found","message":"agent not found","request_id":"req-123"}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s\nwant  %s", got, want)
	}
}

func TestErrorWithoutRequestIDHasEmptyField(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusBadRequest, CodeBadRequest, "nope")
	if got := decodeErrorBody(t, rec).Error.RequestID; got != "" {
		t.Fatalf("request_id = %q, want empty", got)
	}
}

func TestJSON(t *testing.T) {
	t.Run("value", func(t *testing.T) {
		rec := httptest.NewRecorder()
		JSON(rec, http.StatusCreated, map[string]int{"n": 1})
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q", ct)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != `{"n":1}` {
			t.Fatalf("body = %s", got)
		}
	})
	t.Run("nil writes status without body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		JSON(rec, http.StatusNoContent, nil)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	})
}

func newTestRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id")})
	})
	mux.HandleFunc("POST /v1/things", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return NewRouter(mux)
}

func TestRouterUnknownRouteIs404JSON(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Error.Code != CodeNotFound {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestRouterWrongMethodIs405WithAllow(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/things", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	if body := decodeErrorBody(t, rec); body.Error.Code != CodeMethodNotAllowed {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestRouterMatchedRoutePassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/things/abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "abc" {
		t.Fatalf("PathValue id = %q", got["id"])
	}
}

func TestRouterMatchedRouteWithMiddlewareSeesPathValue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.PathValue("id"))
	})
	h := Chain(NewRouter(mux), WithRequestID())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/things/xyz", nil))
	if rec.Body.String() != "xyz" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	tests := []struct {
		name    string
		body    string
		wantErr string // "" means success
		want    payload
	}{
		{name: "valid", body: `{"name":"ann","age":3}`, want: payload{Name: "ann", Age: 3}},
		{name: "whitespace around", body: "  {\"name\":\"ann\"}\n  ", want: payload{Name: "ann"}},
		{name: "empty", body: "", wantErr: "request body is empty"},
		{name: "whitespace only", body: "  \n", wantErr: "request body is empty"},
		{name: "malformed", body: `{"name":`, wantErr: "request body must be valid JSON"},
		{name: "not json", body: `hello`, wantErr: "request body must be valid JSON"},
		{name: "unknown field", body: `{"name":"a","x":1}`, wantErr: `unknown field "x"`},
		{name: "wrong type", body: `{"age":"old"}`, wantErr: `field "age" has the wrong type`},
		{name: "trailing object", body: `{"name":"a"}{"name":"b"}`, wantErr: "request body must contain a single JSON object"},
		{name: "trailing garbage", body: `{"name":"a"} xyz`, wantErr: "request body must contain a single JSON object"},
		{name: "too large", body: `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, wantErr: "request body too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(tc.body))
			rec := httptest.NewRecorder()
			var got payload
			err := DecodeJSON(rec, req, &got)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tc.want {
					t.Fatalf("got %+v, want %+v", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q", tc.wantErr)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDecodeJSONNilBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = nil
	var v map[string]any
	if err := DecodeJSON(httptest.NewRecorder(), req, &v); err == nil || err.Error() != "request body is empty" {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestIDContextEmpty(t *testing.T) {
	if got := RequestID(t.Context()); got != "" {
		t.Fatalf("RequestID = %q, want empty", got)
	}
}

func TestChainOrder(t *testing.T) {
	var trace []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, name+">")
				next.ServeHTTP(w, r)
				trace = append(trace, "<"+name)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace = append(trace, "handler")
	}), mw("a"), mw("b"), mw("c"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	want := "a> b> c> handler <c <b <a"
	if got := strings.Join(trace, " "); got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestNewServerTimeouts(t *testing.T) {
	srv := NewServer(":9999", http.NewServeMux())
	if srv.Addr != ":9999" {
		t.Fatalf("Addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %v", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want 0", srv.WriteTimeout)
	}
}

func TestLoggerRecordsStatusAndBytes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "12345")
	}), WithRequestID(), WithLogger(logger))

	req := httptest.NewRequest(http.MethodGet, "/a/b?secret=1", nil)
	req.Header.Set("X-Request-ID", "abc-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, want := range []string{"method=GET", "path=/a/b", "status=418", "bytes=5", "request_id=abc-1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "secret") {
		t.Fatalf("log leaked query string: %q", line)
	}
}

func TestResponseWriterFlushAndUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newRespWriter(rec)
	rw.Flush()
	if !rec.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
	if rw.Unwrap() != rec {
		t.Fatal("Unwrap does not return the underlying writer")
	}
}

package httpx

import "net/http"

// discardWriter records the status and headers a handler sets, and drops the body.
// NewRouter uses it to probe what the mux's fallback handler would answer.
type discardWriter struct {
	header http.Header
	status int
}

func (d *discardWriter) Header() http.Header {
	return d.header
}

func (d *discardWriter) WriteHeader(code int) {
	if d.status == 0 {
		d.status = code
	}
}

func (d *discardWriter) Write(b []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	return len(b), nil
}

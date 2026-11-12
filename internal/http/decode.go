package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxBodyBytes = 1 << 20 // 1 MiB

const unknownFieldPrefix = "json: unknown field "

var (
	errBodyEmpty       = errors.New("request body is empty")
	errBodyInvalid     = errors.New("request body must be valid JSON")
	errBodyTooLarge    = errors.New("request body too large")
	errBodySingleValue = errors.New("request body must contain a single JSON object")
)

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return errBodyEmpty
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}

	// Anything after the first value (other than whitespace) is an error.
	var extra any
	err := dec.Decode(&extra)
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case isTooLarge(err):
		return errBodyTooLarge
	default:
		return errBodySingleValue
	}
}

// decodeError maps a json/io error to a message that is safe to show clients.
func decodeError(err error) error {
	var ute *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return errBodyEmpty
	case isTooLarge(err):
		return errBodyTooLarge
	case errors.As(err, &ute):
		if ute.Field != "" {
			return fmt.Errorf("field %q has the wrong type", ute.Field)
		}
		return errors.New("request body has a value of the wrong type")
	case strings.HasPrefix(err.Error(), unknownFieldPrefix):
		return errors.New(strings.TrimPrefix(err.Error(), "json: "))
	default:
		return errBodyInvalid
	}
}

func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

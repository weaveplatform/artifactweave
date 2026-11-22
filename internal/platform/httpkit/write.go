package httpkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// WriteErr renders err with the error response profile ErrorProfile gives
// r. An error that is not an *apierror.Error reaches the client as a bare
// 500, so its cause goes to the access log (NoteCause). Cancellation of the
// request itself aborts through net/http without publishing a response.
func WriteErr(w http.ResponseWriter, r *http.Request, err error) {
	WriteErrProfile(w, r, ErrorProfile(r), err)
}

// WriteErrProfile renders err with profile for a handler that owns its wire
// envelope independently of the router. Like WriteErr, it records unexpected
// causes and aborts matching request cancellation before publishing a response.
func WriteErrProfile(w http.ResponseWriter, r *http.Request, profile apierror.Profile, err error) {
	// A disconnected client can cancel a concurrent lookup after another
	// request was refused. Preserve the cause without inventing a server 500.
	if errors.Is(err, context.Canceled) && errors.Is(r.Context().Err(), context.Canceled) {
		NoteCause(r.Context(), err)
		panic(http.ErrAbortHandler)
	}
	var typed *apierror.Error
	if !errors.As(err, &typed) {
		NoteCause(r.Context(), err)
	}
	apierror.Write(w, profile, err)
}

// WriteJSON writes v as a JSON body with status. The status line is out
// once v is encoding, so an encoding failure can only cut the body short.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// EscapePath escapes each segment of the slash-separated path p for a URL
// path, keeping the slashes between segments.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// FormatTime renders t in Artifactory's ISO 8601 form with milliseconds,
// in UTC.
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

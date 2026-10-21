// Package apierror renders errors in the envelope each client surface expects.
// Artifactory, Access, the OCI distribution API, Xray, npm and the Terraform
// registry all use different JSON shapes, and clients read them differently:
// jfrog-client-go prints the whole body after the status (only its release
// bundle import reads errors[0].message, and its Xray scan reads error), the
// Terraform providers join every errors[] entry, and Docker clients read
// errors[].code. One table here is the only place a shape is defined. See
// decision record 0002.
package apierror

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Surface selects the error envelope. The zero value is Artifactory, which is
// the right default for anything under /artifactory.
type Surface int

const (
	// Artifactory is {"errors":[{"status":404,"message":"..."}]}.
	Artifactory Surface = iota
	// Access is {"errors":[{"code":"NOT_FOUND","message":"..."}]}, used by
	// /access and the Projects API.
	Access
	// OCI is {"errors":[{"code":"BLOB_UNKNOWN","message":"...","detail":...}]}.
	OCI
	// Xray is {"error":"..."}; served only by the 501 stubs.
	Xray
	// NPM is {"error":"..."}; npm prints the field verbatim.
	NPM
	// TerraformRegistry is {"errors":["..."]}.
	TerraformRegistry
	// TerraformBackend is the JSON:API error document read by go-tfe.
	TerraformBackend
	// Distribution is {"status_code":404,"message":"...","errors":[{"status":404,"message":"..."}]},
	// the shape JFrog Distribution documents; detail is added when set.
	Distribution
	// ArtifactoryLegacy is the Artifactory envelope plus top-level error and
	// message, documented for cold storage retention:
	// {"errors":[{"status":400,"message":"..."}],"error":"Bad Request","message":"..."}.
	ArtifactoryLegacy
	// ImportExport is the Artifactory envelope plus a top-level error that
	// carries the message, documented for import and export:
	// {"errors":[{"status":400,"message":"..."}],"error":"..."}.
	ImportExport
	// PlatformFederation is {"code":404,"message":"..."}, the HTTP status as
	// an integer code, used by the platform federation service (/pfed).
	PlatformFederation
	// Grid is {"code":"NOT_FOUND","message":"..."}, a symbolic code, with
	// details when set, used by the topology service (/grid).
	Grid
	// Text is a plain-text body, used by the Go proxy: the go command shows
	// an error's body only when it is plain text (decision 0035).
	Text
)

// Error carries everything needed to render on any surface. Code is the OCI
// or Access symbolic code; Artifactory ignores it and uses Status.
type Error struct {
	Status  int
	Code    string
	Message string
	Detail  any
	// NoChallenge distinguishes an upstream refusal from a request to authenticate
	// against this server. Never turn upstream credentials into a local challenge.
	NoChallenge       bool
	RetryAfterSeconds int
}

func (e *Error) Error() string { return e.Message }

// New builds an Error for a status with a message.
func New(status int, message string) *Error {
	return &Error{Status: status, Code: defaultCode(status), Message: message}
}

// ErrNotImplemented is returned by generated UnimplementedStrict stubs for
// operations the spec declares but no service implements yet. The strict
// response error handler renders it as 501 in the surface's envelope, so a
// scaffolded operation is visibly absent rather than a 500.
var ErrNotImplemented = &Error{
	Status:  http.StatusNotImplemented,
	Code:    "NOT_IMPLEMENTED",
	Message: "not implemented by artifactweave yet",
}

// NotSupported is the 501 answer for stubbed JFrog products. The message names
// artifactweave so operators reading a Terraform or jf error know the product
// is absent rather than broken.
func NotSupported(product string) *Error {
	return &Error{
		Status:  http.StatusNotImplemented,
		Code:    "UNSUPPORTED",
		Message: product + " is not supported by artifactweave",
	}
}

// BasicChallenge is the WWW-Authenticate value of a 401: Artifactory's realm.
const BasicChallenge = `Basic realm="Artifactory Realm"`

// Write renders err on the given surface. Errors that are not *Error become a
// 500 with a generic message so internal details never reach a client.
func Write(w http.ResponseWriter, s Surface, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = New(http.StatusInternalServerError, "internal error")
	}
	// Clients send credentials only after a challenge. A handler that needs
	// another scheme (the Docker token flow's Bearer) sets its own first.
	if e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	if e.Status == http.StatusUnauthorized && !e.NoChallenge &&
		w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", BasicChallenge)
	}
	if s == Text {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(e.Status)
		_, _ = w.Write([]byte(e.Message + "\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if s == TerraformBackend {
		w.Header().Set("Content-Type", "application/vnd.api+json")
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(envelope(s, e))
}

func envelope(s Surface, e *Error) any {
	switch s {
	case Access:
		return map[string]any{"errors": []map[string]any{{"code": e.Code, "message": e.Message}}}
	case OCI:
		item := map[string]any{"code": e.Code, "message": e.Message}
		if e.Detail != nil {
			item["detail"] = e.Detail
		}
		return map[string]any{"errors": []map[string]any{item}}
	case Xray, NPM:
		return map[string]any{"error": e.Message}
	case TerraformRegistry:
		return map[string]any{"errors": []string{e.Message}}
	case TerraformBackend:
		return map[string]any{
			"errors": []map[string]string{
				{
					"status": strconv.Itoa(e.Status),
					"title":  http.StatusText(e.Status),
					"detail": e.Message,
				},
			},
		}
	case Distribution:
		body := map[string]any{
			"status_code": e.Status,
			"message":     e.Message,
			"errors":      []map[string]any{{"status": e.Status, "message": e.Message}},
		}
		if e.Detail != nil {
			body["detail"] = e.Detail
		}
		return body
	case ArtifactoryLegacy:
		return map[string]any{
			"errors":  []map[string]any{{"status": e.Status, "message": e.Message}},
			"error":   http.StatusText(e.Status),
			"message": e.Message,
		}
	case ImportExport:
		return map[string]any{
			"errors": []map[string]any{{"status": e.Status, "message": e.Message}},
			"error":  e.Message,
		}
	case PlatformFederation:
		return map[string]any{"code": e.Status, "message": e.Message}
	case Grid:
		body := map[string]any{"code": e.Code, "message": e.Message}
		if e.Detail != nil {
			body["details"] = e.Detail
		}
		return body
	default:
		return map[string]any{
			"errors": []map[string]any{{"status": e.Status, "message": e.Message}},
		}
	}
}

// defaultCode maps a status to the symbolic code Access uses when a handler
// did not supply one. OCI handlers always set their own spec code.
func defaultCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusNotImplemented:
		return "UNSUPPORTED"
	default:
		return "INTERNAL_ERROR"
	}
}

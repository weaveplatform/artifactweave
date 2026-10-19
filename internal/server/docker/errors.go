package docker

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// The error codes of the OCI Distribution Specification v1.1 (code-1 to
// code-14). Docker clients branch on the code, not the message, so every
// registry error carries one of these.
const (
	CodeBlobUnknown         = "BLOB_UNKNOWN"
	CodeBlobUploadInvalid   = "BLOB_UPLOAD_INVALID"
	CodeBlobUploadUnknown   = "BLOB_UPLOAD_UNKNOWN"
	CodeDigestInvalid       = "DIGEST_INVALID"
	CodeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	CodeManifestInvalid     = "MANIFEST_INVALID"
	CodeManifestUnknown     = "MANIFEST_UNKNOWN"
	CodeNameInvalid         = "NAME_INVALID"
	CodeNameUnknown         = "NAME_UNKNOWN"
	CodeSizeInvalid         = "SIZE_INVALID"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeDenied              = "DENIED"
	CodeUnsupported         = "UNSUPPORTED"
	CodeTooManyRequests     = "TOOMANYREQUESTS"
	// CodeTagInvalid is the one code outside the specification's list: a
	// push to a tag that would collide with the layout's own folders
	// (decision 0031).
	CodeTagInvalid = "TAG_INVALID"
)

// Errorf builds a registry error: the status, the specification's code and
// a message for people. Write renders it in the OCI envelope.
func Errorf(status int, code, format string, args ...any) *apierror.Error {
	return &apierror.Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// manifestInvalid is MANIFEST_INVALID as 7.x sends it: the fixed message
// "manifest invalid" and what failed as detail.description (RTFACT-29656).
func manifestInvalid(format string, args ...any) *apierror.Error {
	return &apierror.Error{
		Status: http.StatusBadRequest, Code: CodeManifestInvalid, Message: "manifest invalid",
		Detail: map[string]any{"description": fmt.Sprintf(format, args...)},
	}
}

// manifestUnknown is MANIFEST_UNKNOWN as 7.x sends it: the fixed message
// and the image as the client named it as detail.manifest, without the tag
// or digest (RTFACT-25069, RTFACT-30468, and a KB capture on 7.146.10).
func manifestUnknown(q Request) *apierror.Error {
	return &apierror.Error{
		Status: http.StatusNotFound, Code: CodeManifestUnknown,
		Message: "The named manifest is not known to the registry.",
		Detail:  map[string]any{"manifest": q.addressed()},
	}
}

// blobUnknown is BLOB_UNKNOWN as 7.x sends it: the fixed message, and the
// digest as the client wrote it as detail.blobSum (RTFACT-27328, captured
// on 7.33.8).
func blobUnknown(digest string) *apierror.Error {
	return &apierror.Error{
		Status: http.StatusNotFound, Code: CodeBlobUnknown,
		Message: "blob unknown to registry",
		Detail:  map[string]any{"blobSum": digest},
	}
}

// nameUnknown is NAME_UNKNOWN as 7.x sends it: the fixed message, and as
// detail.name the image as the client named it, as a tags list of an
// unknown image names it (RTFACT-29193, captured on 7.46.11). A catalog
// names a repository and no image, so the repository key stands in.
func nameUnknown(q Request) *apierror.Error {
	name := q.addressed()
	if q.Name == "" {
		name = q.RepoKey
	}
	return &apierror.Error{
		Status: http.StatusNotFound, Code: CodeNameUnknown,
		Message: "Repository name not known to registry.",
		Detail:  map[string]any{"name": name},
	}
}

// Write renders err in the OCI envelope. An error that is not an
// *apierror.Error becomes a 500 without its detail, as everywhere else.
func Write(w http.ResponseWriter, err error) { apierror.Write(w, apierror.OCI, err) }

// WriteAll renders several errors in one OCI envelope with status, as for
// a manifest that references more than one unknown blob: clients report
// each code and detail.
func WriteAll(w http.ResponseWriter, status int, errs ...*apierror.Error) {
	items := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		it := map[string]any{"code": e.Code, "message": e.Message}
		if e.Detail != nil {
			it["detail"] = e.Detail
		}
		items = append(items, it)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": items})
}

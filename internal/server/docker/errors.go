package docker

import (
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
)

// Errorf builds a registry error: the status, the specification's code and
// a message for people. Write renders it in the OCI envelope.
func Errorf(status int, code, format string, args ...any) *apierror.Error {
	return &apierror.Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Write renders err in the OCI envelope. An error that is not an
// *apierror.Error becomes a 500 without its detail, as everywhere else.
func Write(w http.ResponseWriter, err error) { apierror.Write(w, apierror.OCI, err) }

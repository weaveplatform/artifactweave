package kernel

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// ErrStorageQuota refuses project-owned logical growth. It carries no owner or
// usage data and uses each route's existing error envelope. The 403 and generic
// wording are local choices: primary quota docs establish refusal, not its wire
// answer. This is not an authentication challenge, including for Pub clients.
var ErrStorageQuota = &apierror.Error{
	Status: http.StatusForbidden, Code: "FORBIDDEN",
	Message: "project storage quota exceeded", NoChallenge: true,
}

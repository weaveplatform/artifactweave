package kernel

import (
	"errors"
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

// ErrDiskQuota refuses positive storage growth while the measured hot blob
// volume is at or above quotaConfig.diskSpaceLimitPercentage. The status and
// text are 7.161.26's: the XSD documents a 413 and
// RepositoryServiceImpl.assertStorageQuota writes this message back to the
// client, deliberately without the disk usage, which only the log carries.
var ErrDiskQuota = &apierror.Error{
	Status: http.StatusRequestEntityTooLarge, Code: "REQUEST_ENTITY_TOO_LARGE",
	Message: "Datastore disk usage is too high. Contact your Artifactory administrator to add additional " +
		"storage space or change the disk quota limits.",
	NoChallenge: true,
}

// ErrStorageCapacity refuses positive storage growth while an enabled
// quotaConfig cannot measure the volume it limits. Admitting the write would
// let an unmeasured volume pass the limit, so the refusal fails closed and is
// retryable. 7.x documents no answer for this case; the 503 and text are local.
var ErrStorageCapacity = &apierror.Error{
	Status: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE",
	Message: "storage capacity is unavailable for the configured disk quota", NoChallenge: true,
}

// StorageRefusal reports a quota refusal of optional retained storage, a
// remote cache copy or a transformed copy, which a read then serves
// without retaining rather than failing.
func StorageRefusal(err error) bool {
	return errors.Is(err, ErrStorageQuota) || errors.Is(err, ErrDiskQuota) || errors.Is(err, ErrStorageCapacity)
}

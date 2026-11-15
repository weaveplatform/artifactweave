package vcshttp

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// vcsPeerFile is the file filename within the tag or branch ref of a peer
// Artifactory VCS repository: its bytes, within MaxSelectedFileBytes, when
// it last changed and the use to record once it is delivered.
func (s VCSAPI) vcsPeerFile(
	ctx context.Context, a vcsAccess, owner, name, kind, ref, filename string,
) ([]byte, time.Time, func(context.Context) error, error) {
	target, err := PeerFileURL(a.repo, owner, name, kind, ref, filename)
	if err != nil {
		return nil, time.Time{}, nil,
			apierror.New(http.StatusBadRequest, "invalid VCS peer file source")
	}
	permissionPath := owner + "/" + name + "/refs/" + kind + "/" + ref + "/" + filename
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              permissionPath,
		Mutable:           true,
		ExternalURL:       target.String(),
		CheckURL:          vcsExactURL(target),
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		PermissionPath:    permissionPath,
		// v2: the peer's raw file, where v1 cached the ZIP an ext=zip
		// request was answered with.
		Variant: "vcs-peer-file-v2",
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if err := ValidateFile(ctx, body, size); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return nil, time.Time{}, nil, err
	}
	data, err := s.vcsResultBytes(ctx, result, MaxSelectedFileBytes)
	if err != nil {
		return nil, time.Time{}, nil, err
	}
	return data, resultModified(result), result.Used, nil
}

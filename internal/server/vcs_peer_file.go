package server

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	vcshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/vcs/http"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

func (s VCSAPI) vcsPeerFile(
	ctx context.Context, a vcsAccess, owner, name, kind, ref, filename string,
) (io.Reader, int64, error) {
	target, err := vcshttp.PeerFileURL(a.repo, owner, name, kind, ref, filename)
	if err != nil {
		return nil, 0, apierror.New(http.StatusBadRequest, "invalid VCS peer file source")
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
		Variant:           "vcs-peer-file-v1",
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if err := vcshttp.ValidateFileZIP(ctx, body, size, filename); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return nil, 0, err
	}
	data, err := s.vcsResultBytes(ctx, result, vcshttp.MaxFileZIPBytes)
	if err != nil {
		return nil, 0, err
	}
	return &vcsBody{
		ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx, used: result.Used,
	}, int64(len(data)), nil
}

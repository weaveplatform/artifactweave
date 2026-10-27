package source_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitMetadataRootRequiresActualRootGrant(t *testing.T) {
	resource := source.Resource{
		Kind: source.ResourceKindMetadata, Path: ".remote-discovery/list", Method: http.MethodGet,
		PermissionRoot: true, Variant: "root-discovery-v1",
		Validate: func(context.Context, io.ReadSeeker, int64) error { return nil },
	}
	q := request(t, "artifact:up/allowed.jar:r")
	q.Resource = resource
	calls := 0
	current := q.Access
	svc := source.Service{
		Resolve: identity,
		Revalidate: func(_ context.Context, q source.Request, _ source.Permit) error {
			full, allows := q.Resource.Path, current.Allows
			if q.Resource.PermissionRoot {
				full = ""
			} else if q.Resource.PermissionFolder {
				full, allows = q.Resource.PermissionPath, current.AllowsFolder
			}
			if !allows(
				types.ResourceArtifact,
				authz.Repo{Key: q.Repo.Key},
				full,
				types.ActionRead,
			) {
				return source.ErrDenied
			}
			return nil
		},
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			calls++
			return stream(io.NopCloser(strings.NewReader("{}"))), nil
		}),
	}
	if _, err := svc.Read(t.Context(), q); !errors.Is(err, source.ErrDenied) || calls != 0 {
		t.Fatal("unrelated partial scope started root discovery", calls, err)
	}
	q = request(t, "")
	q.Resource, current = resource, q.Access
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Stream.Body.Close()
	if err = result.CheckRead(t.Context(), "allowed.jar"); err != nil {
		t.Fatal(err)
	}
	if err = result.CheckFolder(t.Context(), "packages"); err != nil {
		t.Fatal(err)
	}
	current = request(t, "artifact:up/allowed.jar:r").Access
	if err = result.CheckRead(t.Context(), "allowed.jar"); !errors.Is(err, source.ErrDenied) {
		t.Fatal("root revocation was hidden by surviving file grant", err)
	}
	if _, err = svc.Read(t.Context(), q); !errors.Is(err, source.ErrDenied) || calls != 1 {
		t.Fatal("current root revocation did not fence acquisition", calls, err)
	}
	for _, change := range []func(*source.Resource){
		func(r *source.Resource) { r.Kind = source.ResourceKindArtifact },
		func(r *source.Resource) { r.Method = http.MethodHead },
		func(r *source.Resource) { r.PermissionFolder = true },
		func(r *source.Resource) { r.PermissionPath = "packages" },
		func(r *source.Resource) { r.Variant = "" },
		func(r *source.Resource) { r.Validate = nil },
		func(r *source.Resource) { r.ChecksumOnly = true },
	} {
		q.Resource = resource
		change(&q.Resource)
		if _, err = svc.Read(
			t.Context(),
			q,
		); !errors.Is(err, source.ErrInvalidRequest) ||
			calls != 1 {
			t.Fatal("malformed root authority reached backend", calls, err)
		}
	}
}

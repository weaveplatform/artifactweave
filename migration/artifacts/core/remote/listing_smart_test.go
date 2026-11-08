package remote_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

func TestAccSmartListingUsesPreparedAuthenticatedStorageResource(t *testing.T) {
	_, s, q := listingFixture(t)
	q.Repo.URL = "https://peer.example/artifactory/api/nuget/packages"
	q.Repo.ContentSynchronisation = &repo.ContentSynchronisation{Enabled: true}
	var seen source.Resource
	s.HTTP = listingBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			seen = q.Resource
			body := `{"repo":"packages","path":"/nested","children":[{"uri":"/child","folder":true},{"uri":"/a b.bin","folder":false},{"uri":"/../outside","folder":false}]}`
			response := source.MetadataResponse{BaseURL: q.Resource.ExternalURL}
			if err := q.Resource.ValidateMetadata(
				ctx,
				strings.NewReader(body),
				int64(len(body)),
				response,
			); err != nil {
				return source.Result{}, err
			}
			return source.Result{
				Metadata: response,
				Stream: &source.Stream{
					Status:         200,
					Body:           io.NopCloser(strings.NewReader(body)),
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	out, err := remote.ListFolder(t.Context(), s, q, "nested", &remote.ListingBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		out.Entries,
		[]remote.ListingEntry{
			{Path: "nested/a b.bin", Folder: false},
			{Path: "nested/child", Folder: true},
		},
	) {
		t.Fatal(out.Entries)
	}
	if !seen.RequireSmart ||
		seen.ExternalURL != "https://peer.example/artifactory/api/storage/packages/nested" ||
		seen.Variant != "folder-listing-smart-v1" ||
		seen.PermissionPath != "nested" {
		t.Fatalf("wrong Smart source %+v", seen)
	}
	q.Repo.URL = "https://peer.example/artifactory/packages?ambiguous=true"
	if _, err = remote.ListFolder(
		t.Context(),
		s,
		q,
		"nested",
		&remote.ListingBudget{},
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatal(err)
	}
}

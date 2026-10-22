package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestSmartListingUsesPreparedAuthenticatedStorageResource(t *testing.T) {
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
	out, err := ListFolder(t.Context(), s, q, "nested", &ListingBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		out.Entries,
		[]ListingEntry{{"nested/a b.bin", false}, {"nested/child", true}},
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
	if _, err = ListFolder(
		t.Context(),
		s,
		q,
		"nested",
		&ListingBudget{},
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatal(err)
	}
}

func TestSmartListingRejectsMalformedOrWrongFolderDocuments(t *testing.T) {
	for _, body := range []string{
		`{}`, `[]`, `null`, `{"repo":"other","path":"/","children":[]}`,
		`{"repo":"up","path":"/elsewhere","children":[]}`,
		`{"repo":"up","path":"/","children":null}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x","folder":"true"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":3,"folder":true}]}`,
		`{"repo":"up","path":"/","children":[]}{}`,
		`{"repo":"up","path":"/","children":[` + strings.TrimSuffix(strings.Repeat(`{"uri":"/a","folder":false},`, 10001), ",") + `]}`,
	} {
		if _, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", ""); err == nil {
			t.Fatalf("accepted %.80s", body)
		}
	}
	for _, key := range []string{"up", "up-cache"} {
		body := fmt.Sprintf(`{"repo":%q,"path":"/","children":[]}`, key)
		out, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", "")
		if err != nil || len(out.entries) != 0 {
			t.Fatal(out, err)
		}
	}
	unsafe := `{"repo":"up","path":"/","children":[{"uri":"/..","folder":true},{"uri":"/a/b","folder":true},{"uri":"//host","folder":true},{"uri":"relative","folder":false},{"uri":"/a\\b","folder":false},{"uri":"/a\u0000b","folder":false},{"uri":"/","folder":true}]}`
	if out, err := parseSmartListing(
		t.Context(),
		strings.NewReader(unsafe),
		"up",
		"",
	); err != nil ||
		len(out.entries) != 0 {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parseSmartListing(
		ctx,
		strings.NewReader(`{"repo":"up","path":"/","children":[{"uri":"/a","folder":true}]}`),
		"up",
		"",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := parseSmartListing(t.Context(), failingListingReader{}, "up", ""); err == nil {
		t.Fatal("reader error hidden")
	}
}

package remote

import (
	"context"
	"errors"
	"io"
	"net/url"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestSmartPeerEndpointAuthority(t *testing.T) {
	for _, raw := range []string{"https://peer.example/artifactory/releases", "https://peer.example/artifactory/api/npm/releases/"} {
		peer, err := ParseSmartPeer(repo.Config{URL: raw})
		if err != nil || peer.BaseURL != "https://peer.example/artifactory" ||
			peer.Repository != "releases" {
			t.Fatal(peer, err)
		}
		target, err := peer.StorageURL("org/file name.jar", url.Values{"list": {""}})
		if err != nil ||
			target != "https://peer.example/artifactory/api/storage/releases/org/file%20name.jar?list=" {
			t.Fatal(target, err)
		}
		for _, path := range []string{"../other", "/absolute", "a//b", "a\\b", "a%2fb", "a?b", "a\nb"} {
			if _, err = peer.StorageURL(path, nil); !errors.Is(err, source.ErrInvalidRequest) {
				t.Fatal(path, err)
			}
		}
	}
	for _, raw := range []string{"file:///repo", "https://u:p@peer/repo", "https://peer/", "https://peer/repo?x=1", "https://peer/repo?", "https://peer/repo#x", "https://peer/a/../repo", "https://peer/a//repo", "https://peer/a%2frepo", "https://peer/a%252frepo", "https://peer/repo-cache", "https://peer/api/a/b/repo"} {
		if _, err := ParseSmartPeer(
			repo.Config{URL: raw},
		); !errors.Is(
			err,
			source.ErrInvalidRequest,
		) {
			t.Fatal(raw, err)
		}
	}
	for _, peer := range []SmartPeer{{BaseURL: "https://peer", Repository: ".."}, {BaseURL: "https://peer?x=1", Repository: "repo"}, {BaseURL: "https://peer", Repository: "other/repo"}} {
		if _, err := peer.StorageURL("", nil); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatal(peer, err)
		}
	}
}

func TestMavenPreparedMetadataKeepsAdmission(t *testing.T) {
	w, m := mavenFixture(t)
	q := w.request("listing", source.Metadata)
	q.Resource.Variant = "listing-v1"
	q.Resource.ExpectedContentType = "application/json"
	called := 0
	q.Resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
		called++
		b, err := io.ReadAll(body)
		if err != nil || string(b) != "{}" {
			return source.ErrInvalidResult
		}
		return nil
	}
	w.up.Set(
		"/listing",
		fakeupstream.Resource{
			Body:   []byte("{}"),
			Header: map[string][]string{"Content-Type": {"application/json"}},
		},
	)
	result, err := m.Read(t.Context(), q, w.permit)
	if err != nil {
		t.Fatal(err)
	}
	if w.bytes(result) != "{}" || called == 0 {
		t.Fatal("custom admission was replaced", called)
	}
	// A nil pool would panic if the prepared response asked for checksum files.
	w.p.Pool = nil
	if err = m.PrepareUpload(t.Context(), q, w.permit, Options{}, &item.CacheUpload{}); err != nil {
		t.Fatal(err)
	}
	ordinary := w.request("org/acme/app/maven-metadata.xml", source.Metadata)
	prepared, err := m.prepare(t.Context(), ordinary)
	if err != nil || prepared.Resource.Variant != "maven-source-v1" ||
		prepared.Resource.Validate == nil ||
		preparedMetadata(prepared.Resource) {
		t.Fatal("ordinary Maven admission changed", prepared.Resource, err)
	}
}

func TestSmartProtocolRepositoryRoots(t *testing.T) {
	for _, tc := range []struct {
		kind      repo.PackageType
		path, key string
	}{
		{repo.NuGet, "api/nuget/v3/releases/index.json", "releases"},
		{repo.PyPI, "api/pypi/releases/simple/", "releases"},
		{repo.PyPI, "api/pypi/simple", "simple"},
		{repo.Generic, "index.json", "index.json"},
	} {
		peer, err := ParseSmartPeer(
			repo.Config{PackageType: tc.kind, URL: "https://peer.example/artifactory/" + tc.path},
		)
		if err != nil || peer.BaseURL != "https://peer.example/artifactory" ||
			peer.Repository != tc.key {
			t.Fatal(tc, peer, err)
		}
	}
	if _, err := ParseSmartPeer(
		repo.Config{
			PackageType: repo.Generic,
			URL:         "https://peer.example/artifactory/api/nuget/v3/releases/index.json",
		},
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatal("ambiguous generic endpoint", err)
	}
}

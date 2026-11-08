package remote

import (
	"errors"
	"net/url"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

func TestAccSmartPeerEndpointAuthority(t *testing.T) {
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

func TestAccSmartProtocolRepositoryRoots(t *testing.T) {
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

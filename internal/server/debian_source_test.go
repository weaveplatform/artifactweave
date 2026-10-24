package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestDebianRemoteSourceRejectsInvalidPackages(t *testing.T) {
	for _, tc := range []struct {
		path    string
		fixture string
	}{
		{"pool/alpha_1.0_amd64_0.deb", "alpha_1.0_amd64_0.deb"},
		{"pool/alpha_1.0-1.dsc", "alpha_1.0-1.dsc"},
	} {
		q, err := debianSourceRequest(source.Request{
			Repo:     repo.Config{PackageType: repo.Debian},
			Resource: source.Resource{Path: tc.path, Method: http.MethodGet},
		})
		if err != nil || q.Resource.Validate == nil || q.Resource.Variant == "" {
			t.Fatalf("%s: missing remote validator: %v", tc.path, err)
		}
		body, err := os.ReadFile(filepath.Join("..", "format", "debian", "testdata", tc.fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Resource.Validate(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
		); err != nil {
			t.Fatalf("%s: genuine package rejected: %v", tc.path, err)
		}
		body[0] = 0
		err = q.Resource.Validate(context.Background(), bytes.NewReader(body), int64(len(body)))
		var upstream *source.HTTPError
		if !errors.As(err, &upstream) || upstream.Status != http.StatusBadGateway ||
			upstream.Kind != source.IntegrityFailure {
			t.Errorf("%s: corrupt upstream package returned %v", tc.path, err)
		}
	}
	for _, p := range []string{"../outside.deb", "pool/bad name.deb", "pool/../bad.dsc"} {
		_, err := debianSourceRequest(source.Request{
			Repo:     repo.Config{PackageType: repo.Debian},
			Resource: source.Resource{Path: p, Method: http.MethodGet},
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unsafe remote path %q: %v", p, err)
		}
	}
}

func TestDebianRemoteMetadataIsFetchableAndVerified(t *testing.T) {
	for _, path := range []string{
		"dists/stable/InRelease",
		"dists/stable/Release",
		"dists/stable/Release.gpg",
		"dists/stable/main/binary-amd64/Packages.gz",
		"dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("a", 64),
	} {
		q, err := debianSourceRequest(source.Request{
			Repo:     repo.Config{PackageType: repo.Debian},
			Resource: source.Resource{Path: path, Method: http.MethodGet},
		})
		if err != nil || q.Resource.Kind != source.Metadata ||
			q.Resource.ValidateMetadata == nil || q.Resource.Variant == "" {
			t.Errorf("%s remains a cache-only derived item: %v, %+v", path, err, q.Resource)
		}
	}
	path := "dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("a", 64)
	q, err := debianSourceRequest(source.Request{
		Repo:     repo.Config{PackageType: repo.Debian},
		Resource: source.Resource{Path: path, Method: http.MethodGet},
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte("wrong by-hash bytes")
	err = q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(bad),
		int64(len(bad)), source.MetadataResponse{})
	var upstream *source.HTTPError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusBadGateway {
		t.Fatalf("wrong by-hash bytes were accepted: %v", err)
	}
}

package hex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitTarballPathGrammar(t *testing.T) {
	for _, tc := range []struct{ path, name, version string }{
		{"tarballs/decimal-2.0.0.tar", "decimal", "2.0.0"},
		{"tarballs/my-lib-1.0.0-rc.1.tar", "my-lib", "1.0.0-rc.1"},
		{"tarballs/../secret.tar", "", ""},
		{"tarballs/pkg.tar", "", ""},
		{"other/decimal-2.0.0.tar", "", ""},
		{"tarballs/decimal-2.0.0.tar.gz", "", ""},
	} {
		name, version, ok := splitTarball(tc.path)
		if ok != (tc.name != "") || name != tc.name || version != tc.version {
			t.Errorf("%s => %q %q %v", tc.path, name, version, ok)
		}
	}
}

func TestUnitRebindReleasePreservesExternalDependencies(t *testing.T) {
	original := hexfmt.Package{Requirements: []hexfmt.Requirement{
		{Package: "telemetry", Repository: "hexpm"},
		{Package: "external", Repository: "other"},
		{Package: "implicit"},
	}}
	remote := rebindRelease(original, "hexpm", "hex-remote")
	virtual := rebindRelease(remote, "hex-remote", "hex-virtual")
	if got := virtual.Requirements[0].Repository; got != "hex-virtual" {
		t.Errorf("same-source dependency bypasses virtual repository: %q", got)
	}
	if got := virtual.Requirements[1].Repository; got != "other" {
		t.Errorf("explicit external dependency changed: %q", got)
	}
	if got := virtual.Requirements[2].Repository; got != "" {
		t.Errorf("implicit dependency changed: %q", got)
	}
	if original.Requirements[0].Repository != "hexpm" ||
		remote.Requirements[0].Repository != "hex-remote" {
		t.Fatal("rebind mutated a source registry view")
	}
	if unchanged := rebindRelease(
		original,
		"hexpm",
		"hexpm",
	); unchanged.Requirements[0].Repository != "hexpm" {
		t.Fatal("identity rebind changed a dependency")
	}
	if unchanged := rebindRelease(
		original,
		"",
		"hex-remote",
	); unchanged.Requirements[0].Repository != "hexpm" {
		t.Fatal("unknown upstream rebind changed a dependency")
	}
}

func TestUnitCompactAdvisoriesExcludesDeniedReleaseMetadata(t *testing.T) {
	reg := hexfmt.RegistryPackage{
		Advisories: [][]byte{[]byte("hidden"), []byte("visible")},
		Releases: []hexfmt.Package{
			{Version: "2.0.0", AdvisoryIndexes: []uint64{1}},
			{Version: "2.0.1", AdvisoryIndexes: []uint64{1}},
		},
	}
	filtered := compactAdvisories(reg)
	if len(filtered.Advisories) != 1 || string(filtered.Advisories[0]) != "visible" ||
		len(filtered.Releases[0].AdvisoryIndexes) != 1 ||
		filtered.Releases[0].AdvisoryIndexes[0] != 0 {
		t.Fatalf("filtered registry retained denied advisory: %+v", filtered)
	}
	if filtered.Releases[1].AdvisoryIndexes[0] != 0 {
		t.Fatal("shared advisory was not remapped consistently")
	}
	if len(reg.Advisories) != 2 || reg.Releases[0].AdvisoryIndexes[0] != 1 {
		t.Fatal("filter mutated original signed registry metadata")
	}
}

func TestUnitRemoteTarballBoundaryRejectsUnverifiedSourceStream(t *testing.T) {
	fixture, err := os.ReadFile("../../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	h := handler{}
	r := (&http.Request{}).WithContext(t.Context())
	valid := source.Result{Stream: &source.Stream{
		Body: io.NopCloser(bytes.NewReader(fixture)), Status: http.StatusOK,
	}}
	if _, err := h.validateRemoteTarball(r, &valid, "decimal", "2.0.0", expected); err != nil {
		t.Fatalf("valid source body: %v", err)
	}
	replayed, err := io.ReadAll(valid.Stream.Body)
	if err != nil || !bytes.Equal(replayed, fixture) || valid.Stream.Size != int64(len(fixture)) {
		t.Fatalf("source body was not replayable: %d bytes, %v", len(replayed), err)
	}
	corrupt := bytes.Clone(fixture)
	corrupt[100] ^= 1
	for _, tc := range []struct {
		name    string
		result  source.Result
		version string
	}{
		{"empty result", source.Result{}, "2.0.0"},
		{"empty stream", source.Result{Stream: &source.Stream{Status: http.StatusOK}}, "2.0.0"},
		{"partial status", source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(fixture)), Status: http.StatusPartialContent,
		}}, "2.0.0"},
		{"corrupt archive", source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(corrupt)), Status: http.StatusOK,
		}}, "2.0.0"},
		{"identity mismatch", source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(fixture)), Status: http.StatusOK,
		}}, "2.0.1"},
		{"empty body", source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(nil)), Status: http.StatusOK,
		}}, "2.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.validateRemoteTarball(
				r,
				&tc.result,
				"decimal",
				tc.version,
				expected,
			); err == nil {
				t.Fatal("accepted unverified source bytes")
			}
		})
	}
	if _, err := h.remoteResult(r, "tarballs/bad.tar"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("noncanonical tarball path: %v", err)
	}
	if _, err := (virtualSource{}).Item(
		t.Context(),
		nil,
		repo.Config{},
		"",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatalf("unexpected virtual item source: %v", err)
	}
}

func TestUnitRemoteTarballSourceValidatorChecksIdentityAndBounds(t *testing.T) {
	archive, err := os.ReadFile("../../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := hexfmt.InspectTarball(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodGet,
		"/artifactory/api/hex/hex-r/tarballs/decimal-2.0.0.tar",
		nil,
	)
	resource := tarballResource(r, "tarballs/decimal-2.0.0.tar", "decimal", "2.0.0", expected)
	if err := resource.Validate(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
	); err != nil {
		t.Fatalf("valid source archive rejected: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := resource.Validate(
		canceled,
		bytes.NewReader(archive),
		int64(len(archive)),
	); err == nil {
		t.Fatal("canceled source validation accepted")
	}
	if err := resource.Validate(
		t.Context(),
		bytes.NewReader(archive),
		hexfmt.MaxTarballBytes+1,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("oversized source archive accepted: %v", err)
	}
	wrong := tarballResource(r, "tarballs/decimal-2.0.1.tar", "decimal", "2.0.1", expected)
	if err := wrong.Validate(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("wrong signed package identity accepted: %v", err)
	}
}

package itemkit_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// debianStanza is a Packages index with one amd64 stanza naming file.
func debianStanza(file string) []byte {
	return []byte("Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: " + file +
		"\nSize: 4\nSHA256: " + strings.Repeat("a", 64) + "\n\n")
}

// debianForm is body in the form suffix names, as a repository writes it.
func debianForm(t *testing.T, body []byte, suffix string) []byte {
	t.Helper()
	forms, err := debian.IndexForms(body, []string{"bz2", "lzma", "xz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range forms {
		if form.Suffix == suffix {
			return form.Body
		}
	}
	t.Fatalf("no %q form", suffix)
	return nil
}

func debianRequest(p, query string) (source.Request, error) {
	return itemkit.DebianSourceRequest(source.Request{
		Resource: source.Resource{Path: p, Query: query, Method: http.MethodGet},
	})
}

func wantIntegrityFailure(t *testing.T, name string, err error) {
	t.Helper()
	var failure *source.HTTPError
	if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
		failure.Kind != source.FailureKindIntegrity {
		t.Errorf("%s: %v; want a 502 integrity failure", name, err)
	}
}

// zeros reads n zero bytes.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), z.n))
	clear(p[:n])
	z.n -= int64(n)
	return n, nil
}

func (z *zeros) Seek(int64, int) (int64, error) { return 0, nil }

// TestUnitDebianSourceRequestNamesWhatAptReadsFromAnUpstream: a remote
// Debian repository's source request admits the paths apt reads that it
// answered not found before #1273, without asking the upstream: an
// installer's Packages index, as metadata checked as an index of .udeb
// files and read with the permission on its own path; any by-hash file,
// as metadata checked against its name's SHA-256; a .lzma index; and a
// package under a folder named by-hash or whose name starts Packages, by
// its own grammar. A translation still passes through unchecked.
func TestUnitDebianSourceRequestNamesWhatAptReadsFromAnUpstream(t *testing.T) {
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	translation := []byte("Package: alpha\nDescription-md5: x\nDescription-en: alpha\n\n")
	sum := sha256.Sum256(translation)
	translationByHash := "dists/stable/main/i18n/by-hash/SHA256/" + hex.EncodeToString(sum[:])
	contentsByHash := "dists/stable/main/by-hash/SHA256/" + hex.EncodeToString(sum[:])
	udebIndex := debianForm(t, debianStanza("pool/main/a/alpha/alpha_1.0_amd64.udeb"), ".gz")
	lzmaIndex := debianForm(t, debianStanza("pool/main/a/alpha/alpha_1.0_amd64.deb"), ".lzma")
	for _, tc := range []struct {
		name, path, variant string
		// kind is the zero kind for a package, which the source service
		// reads as an artifact.
		kind             source.ResourceKind
		permissionFolder bool
		valid            []byte
	}{
		{
			"installer index", "dists/stable/main/debian-installer/binary-amd64/Packages.gz",
			"debian-installer-packages-v1", source.ResourceKindMetadata, false, udebIndex,
		},
		{
			"translation's by-hash copy", translationByHash, "debian-by-hash-v1",
			source.ResourceKindMetadata, false, translation,
		},
		{
			"Contents index's by-hash copy", contentsByHash, "debian-by-hash-v1",
			source.ResourceKindMetadata, false, translation,
		},
		{
			"LZMA index", "dists/stable/main/binary-amd64/Packages.lzma", "debian-packages-v2",
			source.ResourceKindMetadata, true, lzmaIndex,
		},
		{
			"package under a by-hash folder", "pool/main/by-hash/SHA256/alpha_1.0_amd64.deb",
			"debian-binary-v1", 0, false, deb,
		},
		{
			"package whose name starts Packages", "pool/main/p/Packages-tool_1.0_amd64.deb",
			"debian-binary-v1", 0, false, deb,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := debianRequest(tc.path, "")
			if err != nil || q.Resource.Kind != tc.kind || q.Resource.Variant != tc.variant ||
				q.Resource.PermissionFolder != tc.permissionFolder {
				t.Fatalf("%+v, %v", q.Resource, err)
			}
			validate := func(body []byte, size int64) error {
				if q.Resource.ValidateMetadata != nil {
					return q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(body), size,
						source.MetadataResponse{})
				}
				return q.Resource.Validate(t.Context(), bytes.NewReader(body), size)
			}
			if err := validate(tc.valid, int64(len(tc.valid))); err != nil {
				t.Errorf("upstream's own file: %v", err)
			}
			corrupt := append([]byte{0}, tc.valid[1:]...)
			wantIntegrityFailure(t, "corrupt file", validate(corrupt, int64(len(corrupt))))
			wantIntegrityFailure(t, "file short of its size",
				validate(tc.valid, int64(len(tc.valid))+1))
		})
	}
	q, err := debianRequest("dists/stable/main/i18n/Translation-en.bz2", "")
	if err != nil || q.Resource.Kind != 0 || q.Resource.Variant != "" ||
		q.Resource.Validate != nil || q.Resource.ValidateMetadata != nil {
		t.Errorf("translation: %+v, %v", q.Resource, err)
	}
	q, err = debianRequest("dists/stable/main/debian-installer/binary-amd64/Packages", "")
	if err != nil {
		t.Fatal(err)
	}
	wantIntegrityFailure(t, "installer index naming a .deb", q.Resource.ValidateMetadata(
		t.Context(), bytes.NewReader(debianStanza("pool/a.deb")), int64(len(debianStanza("pool/a.deb"))),
		source.MetadataResponse{}))
}

// TestUnitDebianSourceRequestBoundsOnlyAnIndexCopy: a binary-<arch> or
// source index's by-hash copy keeps the 64 MiB bound of the route that
// reads it whole; any other by-hash file is hashed as it streams and may
// be larger. An empty by-hash file is refused, as before.
func TestUnitDebianSourceRequestBoundsOnlyAnIndexCopy(t *testing.T) {
	const size = 64<<20 + 1
	sum := sha256.New()
	if _, err := io.Copy(sum, &zeros{n: size}); err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(sum.Sum(nil))
	for _, tc := range []struct {
		path  string
		valid bool
	}{
		{"dists/stable/main/binary-amd64/by-hash/SHA256/" + name, false},
		{"dists/stable/main/source/by-hash/SHA256/" + name, false},
		{"dists/stable/main/by-hash/SHA256/" + name, true},
	} {
		q, err := debianRequest(tc.path, "")
		if err != nil {
			t.Fatal(err)
		}
		err = q.Resource.ValidateMetadata(t.Context(), &zeros{n: size}, size, source.MetadataResponse{})
		if tc.valid && err != nil {
			t.Errorf("%s: %v", tc.path, err)
		}
		if !tc.valid {
			wantIntegrityFailure(t, tc.path, err)
		}
	}
	empty := sha256.Sum256(nil)
	q, err := debianRequest("dists/stable/main/by-hash/SHA256/"+hex.EncodeToString(empty[:]), "")
	if err != nil {
		t.Fatal(err)
	}
	wantIntegrityFailure(t, "empty by-hash file", q.Resource.ValidateMetadata(t.Context(),
		bytes.NewReader(nil), 0, source.MetadataResponse{}))
}

// TestUnitDebianSourceRequestKeepsRefusingWhatItCannotName: a by-hash name
// that is no SHA-256, a Packages name of no index apt reads, an index or
// by-hash path with a query, and a package path its grammar refuses still
// answer not found without asking the upstream.
func TestUnitDebianSourceRequestKeepsRefusingWhatItCannotName(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, tc := range []struct{ path, query string }{
		{"dists/stable/main/i18n/by-hash/SHA256/abc", ""},
		{"dists/stable/main/i18n/by-hash/SHA256/" + sum, "x=1"},
		{"dists/stable/main/debian-installer/binary-amd64/Packages.lz4", ""},
		{"dists/stable/main/debian-installer/binary-amd64/Packages", "x=1"},
		{"dists/stable/main/binary-amd64/Packages.lzma", "x=1"},
		{"dists/stable/Packages", ""},
		{"pool/bad name.deb", ""},
		{"pool/../bad.dsc", ""},
	} {
		if _, err := debianRequest(tc.path, tc.query); !errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s?%s: %v; want ErrNotFound", tc.path, tc.query, err)
		}
	}
}

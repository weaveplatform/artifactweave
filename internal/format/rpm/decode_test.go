package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func metadataFixture(t *testing.T) (Package, Metadata, string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	const root = "channel/"
	pkg, err := ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), root+fixtureName)
	if err != nil {
		t.Fatal(err)
	}
	pkg.SHA256 = hexSHA256(body)
	meta, err := EncodeMetadata([]Package{pkg}, root, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return pkg, meta, root
}

func primaryBytes(t *testing.T, meta Metadata, root string) (primaryDoc, string) {
	t.Helper()
	p, _, err := DecodeRepomd(meta.Files["repodata/repomd.xml"], root)
	if err != nil {
		t.Fatal(err)
	}
	compressed := meta.Files[strings.TrimPrefix(p, root)]
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	var doc primaryDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc, p
}

func compressPrimary(t *testing.T, doc primaryDoc) ([]byte, string) {
	t.Helper()
	raw, err := xml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes(), fmt.Sprintf("%x", sha256.Sum256(compressed.Bytes()))
}

func TestMetadataRejectsUntrustedReferences(t *testing.T) {
	t.Parallel()
	_, meta, root := metadataFixture(t)
	var original repoDoc
	if err := xml.Unmarshal(meta.Files["repodata/repomd.xml"], &original); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*repoDoc)
	}{
		{"wrong namespace", func(d *repoDoc) { d.Common = "https://other.example/metadata" }},
		{"missing primary", func(d *repoDoc) { d.Data[0].Type = "other" }},
		{"wrong digest type", func(d *repoDoc) { d.Data[0].Checksum.Type = "sha1" }},
		{"invalid digest", func(d *repoDoc) { d.Data[0].Checksum.Value = strings.Repeat("z", 64) }},
		{"escape metadata directory", func(d *repoDoc) { d.Data[0].Location.Href = "../outside.xml.gz" }},
		{"digest not bound to filename", func(d *repoDoc) { d.Data[0].Location.Href = "repodata/other-primary.xml.gz" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := original
			copy.Data = append([]repoData(nil), original.Data...)
			tc.mutate(&copy)
			body, err := xml.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := DecodeRepomd(body, root); err == nil {
				t.Fatal("untrusted repomd reference accepted")
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("<not-xml"), bytes.Repeat([]byte("x"), maxMetadataBytes+1)} {
		if _, _, err := DecodeRepomd(body, root); err == nil {
			t.Fatal("invalid repomd accepted")
		}
	}
}

func TestPrimaryRejectsUntrustedPackages(t *testing.T) {
	t.Parallel()
	_, meta, root := metadataFixture(t)
	original, primaryPath := primaryBytes(t, meta, root)
	for _, tc := range []struct {
		name   string
		mutate func(*primaryDoc)
	}{
		{"wrong namespace", func(d *primaryDoc) { d.Common = "https://other.example/metadata" }},
		{"wrong package count", func(d *primaryDoc) { d.Packages++ }},
		{"wrong package type", func(d *primaryDoc) { d.Items[0].Type = "deb" }},
		{"wrong digest type", func(d *primaryDoc) { d.Items[0].Checksum.Type = "sha1" }},
		{"malformed package digest", func(d *primaryDoc) { d.Items[0].Checksum.Value = "missing" }},
		{"traversal", func(d *primaryDoc) { d.Items[0].Location.Href = "../outside.rpm" }},
		{"identity mismatch", func(d *primaryDoc) { d.Items[0].Version.Ver = "other" }},
		{"duplicate package", func(d *primaryDoc) {
			d.Items = append(d.Items, d.Items[0])
			d.Packages = len(d.Items)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := original
			copy.Items = append([]primaryPackage(nil), original.Items...)
			tc.mutate(&copy)
			compressed, digest := compressPrimary(t, copy)
			if _, err := DecodePrimary(compressed, root, digest); err == nil {
				t.Fatal("untrusted primary package accepted")
			}
		})
	}
	valid := meta.Files[strings.TrimPrefix(primaryPath, root)]
	for _, body := range [][]byte{nil, []byte("not gzip"), bytes.Repeat([]byte("x"), maxMetadataBytes+1)} {
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err := DecodePrimary(body, root, digest); err == nil {
			t.Fatal("invalid primary body accepted")
		}
	}
	if _, err := DecodePrimary(valid, root, strings.Repeat("0", 64)); err == nil {
		t.Fatal("primary checksum mismatch accepted")
	}
}

func TestDependencyConstraintsRoundTripThroughPrimary(t *testing.T) {
	t.Parallel()
	pkg, _, root := metadataFixture(t)
	pkg.Requires = []Dependency{
		{Name: "unversioned"},
		{Name: "less", Version: "1.0", Flags: 2},
		{Name: "greater", Version: "1.0-2", Flags: 4},
		{Name: "equal", Version: "2:1.0-2", Flags: 8},
		{Name: "less-equal", Version: "3:2.0", Flags: 10},
		{Name: "greater-equal", Version: "3.0-4", Flags: 12},
	}
	pkg.Provides = []Dependency{{Name: "provided", Version: "1.0", Flags: 8}}
	pkg.Conflicts = []Dependency{{Name: "conflict", Version: "2.0", Flags: 2}}
	pkg.Obsoletes = []Dependency{{Name: "obsolete", Version: "3.0", Flags: 4}}
	meta, err := EncodeMetadata([]Package{pkg}, root, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	path, digest, err := DecodeRepomd(meta.Files["repodata/repomd.xml"], root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePrimary(meta.Files[strings.TrimPrefix(path, root)], root, digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Requires, pkg.Requires) ||
		!reflect.DeepEqual(got[0].Provides, pkg.Provides) ||
		!reflect.DeepEqual(got[0].Conflicts, pkg.Conflicts) ||
		!reflect.DeepEqual(got[0].Obsoletes, pkg.Obsoletes) {
		t.Fatalf("dependency constraints changed during metadata round trip: %+v", got)
	}
	// XML namespace prefixes are labels chosen by an upstream publisher. The
	// repository must consume the RPM namespace even when its prefix changes.
	gz, err := gzip.NewReader(bytes.NewReader(meta.Files[strings.TrimPrefix(path, root)]))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("rpm:"), []byte("vendor:"))
	raw = bytes.ReplaceAll(raw, []byte("xmlns:rpm="), []byte("xmlns:vendor="))
	var alternate bytes.Buffer
	w := gzip.NewWriter(&alternate)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	alternateDigest := fmt.Sprintf("%x", sha256.Sum256(alternate.Bytes()))
	got, err = DecodePrimary(alternate.Bytes(), root, alternateDigest)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Requires, pkg.Requires) {
		t.Fatalf("equivalent RPM namespace prefix changed dependencies: %+v, %v", got, err)
	}
}

func TestEncodeMetadataRejectsUnsafePackageRecords(t *testing.T) {
	t.Parallel()
	pkg, _, root := metadataFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*Package) string
	}{
		{"short package checksum", func(p *Package) string { p.SHA256 = "short"; return root }},
		{"unsafe package path", func(p *Package) string { p.Path = "../" + p.Path; return root }},
		{"outside metadata root", func(*Package) string { return "other/" }},
		{"empty relative path", func(p *Package) string { return p.Path }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := pkg
			selectedRoot := tc.mutate(&copy)
			if _, err := EncodeMetadata(
				[]Package{copy},
				selectedRoot,
				time.Unix(1700000000, 0),
			); err == nil {
				t.Fatal("unsafe package metadata accepted")
			}
		})
	}
}

func TestDecodePrimaryRejectsMalformedCompressedXML(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{
		[]byte("<not-xml"), []byte("<metadata><package>"),
		bytes.Repeat([]byte("x"), maxMetadataBytes+1),
	} {
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		if _, err := gz.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		body := compressed.Bytes()
		digest := fmt.Sprintf("%x", sha256.Sum256(body))
		if _, err := DecodePrimary(body, "", digest); err == nil {
			t.Fatal("malformed or oversized primary XML accepted")
		}
	}
}

func TestDecodeIndependentCreaterepoCMetadata(t *testing.T) {
	t.Parallel()
	const fixture = "../../../test/acceptance/testdata/rpm-upstream/repodata"
	manifest, err := os.ReadFile(filepath.Join(fixture, "repomd.xml"))
	if err != nil {
		t.Fatal(err)
	}
	primaryPath, checksum, err := DecodeRepomd(manifest, "")
	if err != nil {
		t.Fatalf("createrepo_c repomd rejected: %v", err)
	}
	primary, err := os.ReadFile(filepath.Join(fixture, filepath.Base(primaryPath)))
	if err != nil {
		t.Fatal(err)
	}
	packages, err := DecodePrimary(primary, "", checksum)
	if err != nil {
		t.Fatalf("createrepo_c primary rejected: %v", err)
	}
	if len(packages) != 1 || packages[0].Path != "pool/"+fixtureName ||
		packages[0].Name != "artifactweave-alpha" || packages[0].Version != "1.0" ||
		packages[0].Release != "1.fc42" || len(packages[0].Provides) == 0 {
		t.Fatalf("createrepo_c package identity or dependencies lost: %+v", packages)
	}
}

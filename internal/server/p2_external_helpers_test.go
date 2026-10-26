package server

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitP2ExternalChildURLRequiresMemberOriginAndSafePath(t *testing.T) {
	member := repo.Config{URL: "https://root.example/site/", P2ChildOrigins: []string{
		"https://child.example",
	}}
	parent, _ := url.Parse("https://root.example/site/compositeContent.xml")
	for _, tc := range []struct {
		location, want string
	}{
		{"nested/site?revision=1", "https://root.example/site/nested/site?revision=1"},
		{"https://child.example/site?revision=1", "https://child.example/site?revision=1"},
		{"https://root.example:443/another", "https://root.example:443/another"},
	} {
		child, err := p2ChildURL(member, parent, tc.location)
		if err != nil || child.String() != tc.want {
			t.Fatalf("%s: %v, %v", tc.location, child, err)
		}
	}
	for _, location := range []string{
		"https://other.example/site", "https://user:secret@child.example/site",
		"https://child.example/site#fragment", "https://child.example/%2e%2e/secret",
		"../secret", strings.Repeat("a", p2.MaxChildURL+1),
	} {
		if child, err := p2ChildURL(member, parent, location); err == nil || child != nil {
			t.Fatalf("unsafe child admitted: %s => %v", location, child)
		}
	}
	if child, err := p2ChildURL(member, nil, "safe"); err == nil || child != nil {
		t.Fatalf("missing parent admitted: %v", child)
	}
	member.P2ChildOrigins = nil
	if child, err := p2ChildURL(
		member,
		parent,
		"https://child.example/site",
	); err == nil ||
		child != nil {
		t.Fatalf("member borrowed revoked origin: %v", child)
	}
	member.URL = "not a source URL"
	if p2AllowedOrigin(member, parent) {
		t.Fatal("invalid source origin admitted")
	}
}

func TestUnitP2ExternalDigestBindsSourceParentAndExactQuery(t *testing.T) {
	id := types.SourceIdentity{ID: "source-A"}
	first := p2ChildDigest(id, "root/site", "https://child.example/site?revision=1")
	if len(first) != 64 {
		t.Fatalf("unexpected child digest %q", first)
	}
	for _, changed := range []string{
		p2ChildDigest(id, "other/site", "https://child.example/site?revision=1"),
		p2ChildDigest(id, "root/site", "https://child.example/site?revision=2"),
		p2ChildDigest(types.SourceIdentity{ID: "source-B"}, "root/site", "https://child.example/site?revision=1"),
	} {
		if changed == first {
			t.Fatal("source, parent, or exact query mutation retained child route")
		}
	}
	for _, raw := range []string{
		"https://ROOT.example/site/?revision=1", "https://root.example/site/?revision=2",
	} {
		u, _ := url.Parse(raw)
		if p2Origin(u) != "https://root.example:443" {
			t.Fatalf("unexpected normalized origin %s", p2Origin(u))
		}
		if p2CycleKey(u) != "https://"+u.Host+"/site" {
			t.Fatalf("cycle key retained query or trailing slash: %s", p2CycleKey(u))
		}
	}
	httpChild, _ := url.Parse("http://child.example/site")
	if p2Origin(httpChild) != "http://child.example:80" {
		t.Fatalf("HTTP default port was not normalized: %s", p2Origin(httpChild))
	}
}

func TestUnitP2ExternalResourcePathsAndArchiveSuffix(t *testing.T) {
	for _, tc := range []struct {
		base, resource, target, entry string
	}{
		{"https://child.example/site?token=opaque", "content.jar", "https://child.example/site/content.jar?token=opaque", ""},
		{"https://child.example/site.zip!/nested", "content.jar", "https://child.example/site.zip", "nested/content.jar"},
	} {
		site, _ := url.Parse(tc.base)
		got, entry, err := p2ChildResourceURL(site, tc.resource)
		if err != nil || got.String() != tc.target || entry != tc.entry {
			t.Fatalf("%s %s: %v, %s, %v", tc.base, tc.resource, got, entry, err)
		}
	}
	base, err := p2RootBase(repo.Config{URL: "https://root.example/site/content.jar?token=opaque"})
	if err != nil || base.String() != "https://root.example/site/" {
		t.Fatalf("root metadata URL not normalized: %v, %v", base, err)
	}
	if _, err := p2RootBase(repo.Config{URL: "://broken"}); err == nil {
		t.Fatal("invalid root URL admitted")
	}
	base, err = p2RootBase(repo.Config{URL: "https://root.example/site"})
	if err != nil || base.String() != "https://root.example/site/" {
		t.Fatalf("directory root was not normalized: %v, %v", base, err)
	}
	site, _ := url.Parse("https://child.example/site/")
	if _, _, err := p2ChildResourceURL(site, "../secret"); err == nil {
		t.Fatal("traversal resource admitted")
	}
	if !p2CompositeName("compositeArtifacts.xml.xz") ||
		p2CompositeName("nested/compositeContent.xml") ||
		p2CompositeName("content.jar") {
		t.Fatal("composite route classifier missed a security boundary")
	}
	if got := p2CompositeCandidates("p2.index"); len(got) != 6 ||
		got[0] != "compositeContent.xml" || got[3] != "compositeArtifacts.xml" {
		t.Fatalf("P2 index lost either composite family: %v", got)
	}
	if got := p2CompositeCandidates("plugins/demo.jar"); len(got) != 3 ||
		got[0] != "compositeArtifacts.xml" {
		t.Fatalf("P2 artifacts route used content parent: %v", got)
	}
}

func TestUnitP2ExternalPathParserRejectsForgedAndOverdeepRoutes(t *testing.T) {
	token := strings.Repeat("a", 64)
	good := "__p2_ext/" + token + "/plugins/plugin_1.0.0.jar"
	parts, name, err := p2ExternalParts(good)
	if err != nil || len(parts) != 1 || parts[0] != token || name != "plugins/plugin_1.0.0.jar" {
		t.Fatalf("valid route lost: %v, %q, %v", parts, name, err)
	}
	for _, path := range []string{
		"__p2_ext/short/content.jar", "__p2_ext/" + strings.ToUpper(token) + "/content.jar",
		"__p2_ext/" + strings.Repeat("z", 64) + "/content.jar",
		"__p2_ext/" + token + "/../content.jar",
		"__p2_ext/" + token,
		"__p2_ext/" + strings.TrimSuffix(strings.Repeat(token+"/", p2.MaxDepth+1), "/") + "/content.jar",
	} {
		if _, _, err := p2ExternalParts(path); err == nil {
			t.Fatalf("forged or overdeep P2 path accepted: %s", path)
		}
	}
}

func TestUnitP2ExternalPayloadChecksumsRejectTampering(t *testing.T) {
	body := []byte("published plugin bytes")
	sum256 := sha256.Sum256(body)
	sum512 := sha512.Sum512(body)
	want := types.Checksums{
		SHA256: hex.EncodeToString(sum256[:]),
		SHA512: hex.EncodeToString(sum512[:]),
	}
	if err := p2CheckBytes(body, want); err != nil {
		t.Fatalf("correct published checksums rejected: %v", err)
	}
	if !errors.Is(p2CheckBytes([]byte("altered plugin bytes"), want), source.ErrInvalidResult) {
		t.Fatal("SHA256 mismatch admitted")
	}
	want.SHA256 = ""
	if !errors.Is(p2CheckBytes([]byte("altered plugin bytes"), want), source.ErrInvalidResult) {
		t.Fatal("SHA512 mismatch admitted")
	}
}

func TestUnitP2ArchiveSuffixRejectsUntrustedTargetShapes(t *testing.T) {
	for _, raw := range []string{
		"://broken!/", "https://user:secret@child.example/site.zip!/",
		"https://child.example/site.zip!/nested", "https://child.example/site.jar!/",
		"https://child.example/site.zip#fragment!/", "file:///site.zip!/",
	} {
		if target, ok := p2ArchiveURL(raw); ok {
			t.Fatalf("unsafe archive suffix admitted: %s => %s", raw, target)
		}
	}
	if target, ok := p2ArchiveURL("https://child.example/site.zip!/"); !ok ||
		target != "https://child.example/site.zip" {
		t.Fatalf("valid archive suffix lost: %s, %t", target, ok)
	}
}

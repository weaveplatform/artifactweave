package bazelmodules

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitSourceRejectsUnapprovedOriginsAndInvalidIntegrity(t *testing.T) {
	c := repo.Config{URL: "https://bcr.example/registry"}
	sum := sha256.Sum256([]byte("archive"))
	doc := sourceDocument{
		Type:      "archive",
		URL:       "https://bcr.example/registry/download/hello.tar.gz",
		Integrity: "sha256-" + base64.StdEncoding.EncodeToString(sum[:]),
	}
	body, _ := json.Marshal(doc)
	parsed, err := parseSource(body, c)
	if err != nil || parsed.sha256() == "" || parsed.archiveFile() != "source.tar.gz" {
		t.Fatal(parsed, err)
	}
	for _, mutate := range []func(*sourceDocument){
		func(d *sourceDocument) { d.URL = "https://evil.example/archive.tar.gz" },
		func(d *sourceDocument) { d.URL = "http://bcr.example/registry/archive.tar.gz" },
		func(d *sourceDocument) { d.URL = "https://user:secret@bcr.example/registry/archive.tar.gz" },
		func(d *sourceDocument) { d.URL = "https://bcr.example/registry/../private/archive.tar.gz" },
		func(d *sourceDocument) { d.Integrity = "sha256-bad" },
		func(d *sourceDocument) { d.Type = "git_repository" },
	} {
		bad := doc
		mutate(&bad)
		raw, _ := json.Marshal(bad)
		if _, err := parseSource(raw, c); err == nil {
			t.Errorf("invalid source admitted: %s", strings.TrimSpace(string(raw)))
		}
	}
	enabled := true
	c.ExternalDependenciesEnabled = &enabled
	c.ExternalDependenciesPatterns = []string{"https://github.com/**"}
	doc.URL = "https://github.com/example/hello/archive/v1.0.0.tar.gz"
	body, _ = json.Marshal(doc)
	if _, err := parseSource(body, c); err != nil {
		t.Fatal(err)
	}
}

func TestUnitMetadataVersionsAreBoundedAndUnique(t *testing.T) {
	for _, body := range []string{`{}`, `{"versions":["1.0.0","1.0.0"]}`, `{"versions":["../evil"]}`, `{"versions":"1.0.0"}`} {
		if _, err := parseMetadata([]byte(body)); err == nil {
			t.Errorf("admitted %s", body)
		}
	}
	versions, err := parseMetadata([]byte(`{"versions":["1.0.0","2.0.0"]}`))
	if err != nil || len(versions) != 2 {
		t.Fatal(versions, err)
	}
}

func TestUnitSourcePatchAndMirrorPolicy(t *testing.T) {
	c := repo.Config{URL: "https://bcr.example/registry"}
	sum := sha256.Sum256([]byte("archive"))
	sri := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
	doc := sourceDocument{
		Type:       "archive",
		URL:        "https://bcr.example/registry/hello.tar.gz",
		Integrity:  sri,
		Patches:    map[string]string{"fix.patch": sri},
		MirrorURLs: []string{"https://bcr.example/registry/mirror.tar.gz"},
	}
	body, _ := json.Marshal(doc)
	parsed, err := parseSource(body, c)
	if err != nil || len(parsed.Patches) != 1 || len(parsed.MirrorURLs) != 0 {
		t.Fatalf("valid patch/mirror metadata %v %+v", err, parsed)
	}
	for _, change := range []func(*sourceDocument){
		func(d *sourceDocument) { d.Patches = map[string]string{"../escape.patch": sri} },
		func(d *sourceDocument) { d.Patches = map[string]string{"fix.patch": "sha256-bad"} },
		func(d *sourceDocument) { d.MirrorURLs = []string{"https://evil.example/archive.tar.gz"} },
		func(d *sourceDocument) { d.StripPrefix = "../escape" },
		func(d *sourceDocument) { d.ArchiveType = "rar" },
	} {
		bad := doc
		change(&bad)
		raw, _ := json.Marshal(bad)
		if _, err := parseSource(raw, c); err == nil {
			t.Errorf("unsafe source accepted: %s", raw)
		}
	}
}

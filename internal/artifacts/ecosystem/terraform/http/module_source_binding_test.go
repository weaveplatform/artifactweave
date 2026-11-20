package terraformhttp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func TestAccChangedModuleSourceControlsFenceIdenticalArchiveBytes(t *testing.T) {
	for _, key := range []string{"remote", "virtual"} {
		for _, control := range []string{"query", "checksum-url", "selector"} {
			t.Run(key+"/"+control, func(t *testing.T) {
				f := newRemoteFixture(t)
				f.modules("1.0.0")
				if key == "virtual" {
					virtualMembers(t, f.fixture, "remote")
				}
				body := moduleZip(t, map[string]string{"one/main.tf": "", "two/main.tf": ""})
				f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/zip"}}})
				oldSource, newSource := "/archives/1.0.0.zip?revision=one", "/archives/1.0.0.zip?revision=two"
				switch control {
				case "checksum-url":
					digest := sha256.Sum256(body)
					for _, name := range []string{"one", "two"} {
						f.up.Set("/SUMS-"+name, fakeupstream.Resource{Body: fmt.Appendf(nil, "%x  1.0.0.zip\n", digest), Header: http.Header{"Content-Type": {"text/plain"}}})
					}
					oldSource = "/archives/1.0.0.zip?checksum=" + url.QueryEscape("file:"+f.up.URL+"/SUMS-one")
					newSource = "/archives/1.0.0.zip?checksum=" + url.QueryEscape("file:"+f.up.URL+"/SUMS-two")
				case "selector":
					oldSource, newSource = "/archives/1.0.0.zip//one", "/archives/1.0.0.zip//two"
				}
				issue := func() string {
					metadata := f.do("GET", prefix+"v1/modules/"+key+"__ns/name/aws/1.0.0/download", "admin")
					expect(t, metadata, 204)
					public, err := url.Parse(metadata.Header().Get("X-Terraform-Get"))
					if err != nil {
						t.Fatal(err)
					}
					public.Path = strings.Split(public.Path, "//")[0]
					return public.String()
				}
				f.location("1.0.0", oldSource)
				old := issue()
				expect(t, f.do("GET", old, ""), 200)
				f.location("1.0.0", newSource)
				f.clock.Advance(6 * time.Second)
				expect(t, f.do("GET", old, ""), 403)
				fresh := issue()
				result := f.do("GET", fresh, "")
				expect(t, result, 200)
				if !bytes.Equal(result.Body.Bytes(), body) {
					t.Fatal("source-control refresh changed identical original bytes")
				}
			})
		}
	}
}

func TestUnitModuleSourceIdentityBindsSnapshotAndChecksumControls(t *testing.T) {
	f := newFixture(t)
	h := handler{o: Options{Keys: f.keys}}
	base := moduleSource{Target: "https://example.test/archive.zip?private=one", Path: "archive.zip", Archive: "zip", Subdir: "modules/one"}
	first := h.moduleSourceDigest(base)
	if first == "" || (handler{}).moduleSourceDigest(base) != "" {
		t.Fatal("source descriptor must require the signing key")
	}
	for name, change := range map[string]func(*moduleSource){
		"query":             func(s *moduleSource) { s.Target = "https://example.test/archive.zip?private=two" },
		"selector":          func(s *moduleSource) { s.Subdir = "modules/two" },
		"checksum URL":      func(s *moduleSource) { s.ChecksumURL = "https://example.test/other-SUMS" },
		"checksum filename": func(s *moduleSource) { s.ChecksumName = "other.zip" },
		"snapshot depth":    func(s *moduleSource) { s.GitHubDepth = "1" },
		"archive hint":      func(s *moduleSource) { s.Archive = "tar.gz" },
	} {
		t.Run(name, func(t *testing.T) {
			next := base
			change(&next)
			if h.moduleSourceDigest(next) == first {
				t.Fatal("source decision changed without changing capability identity")
			}
		})
	}
}

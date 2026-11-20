package replication

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPushEndpointPreservesRepositoryAndEscapesFileNames(t *testing.T) {
	for _, raw := range []string{"https://target.invalid/repo", "https://target.invalid/custom/artifactory/repo/"} {
		endpoint, err := newPushEndpoint(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"nested/file.pkg", "@scope/pkg/a;b?c#d%2F space+é.pkg", "api/storage/other/file", "%2e%2e/%2fhost"} {
			for name, route := range map[string]func(string) (*url.URL, error){"item": endpoint.itemURL, "storage": endpoint.storageURL} {
				u, err := route(path)
				if err != nil {
					t.Fatal(raw, name, path, err)
				}
				prefix := endpoint.prefix + "/repo/"
				if name == "storage" {
					prefix = endpoint.prefix + "/api/storage/repo/"
				}
				reparsed, err := url.Parse(u.String())
				if err != nil || reparsed.Scheme != "https" || reparsed.Host != "target.invalid" || reparsed.Path != prefix+path || reparsed.RawQuery != "" ||
					reparsed.Fragment != "" ||
					reparsed.User != nil {
					t.Fatal("file name escaped its configured namespace", name, path, u, err)
				}
				if strings.Contains(reparsed.EscapedPath(), ";") {
					t.Fatal("file name became a matrix parameter", path, reparsed)
				}
			}
		}
	}
}

func TestUnitPushEndpointRejectsUntrustedReferences(t *testing.T) {
	for _, raw := range []string{
		"https://target.invalid", "https://target.invalid/", "https://target.invalid/repo?",
		"https://user:secret@target.invalid/repo", "https://target.invalid/repo?token=secret",
		"https://target.invalid/repo#other", "https://target.invalid/a/../repo", "https://target.invalid//repo",
		"https://target.invalid/a%2F..%2Frepo", "https://target.invalid/repo%00", "https://target.invalid/%ff",
	} {
		if _, err := newPushEndpoint(raw); !errors.Is(err, kernel.ErrInvalid) {
			t.Fatal("invalid target admitted", raw, err)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("target refusal exposed credentials", err)
		}
	}
	endpoint, err := newPushEndpoint("https://target.invalid/artifactory/repo")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", ".", "..", "a/../b", "//other.invalid/file", "/outside", "trailing/", "a//b", "a\\b", "a\x00b", string([]byte{0xff})} {
		for _, route := range []func(string) (*url.URL, error){endpoint.itemURL, endpoint.storageURL} {
			if _, err := route(path); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("noncanonical item reference admitted", path, err)
			}
		}
	}
	if _, err := (pushEndpoint{}).itemURL("file"); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unconfigured endpoint admitted", err)
	}
}

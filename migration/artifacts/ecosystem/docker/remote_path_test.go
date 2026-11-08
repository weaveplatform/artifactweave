package docker

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
)

// The parser keeps its own copy of the name grammar the transport checks. A
// name one accepts and the other refuses would be cached under a path whose
// image the upstream is never asked for, or refused before it is. Names stay
// within 255 characters: the transport refuses longer ones and the parser
// leaves that to it.
func TestUnitRegistryPathNameGrammarMatchesTransport(t *testing.T) {
	client, err := upstream.NewRegistry(
		repo.Config{URL: "https://registry.example", Class: repo.Remote, PackageType: repo.Docker},
		upstream.RegistryOptions{
			Transport: remote.Options{
				Revision: "test",
				Prepare:  func(*http.Request) error { return nil },
				CheckURL: func(context.Context, *url.URL) error { return nil },
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	names := []string{
		"a", "alpine", "library/alpine", "team/sub/image", "a/b/c/d/e",
		"a.b", "a_b", "a__b", "a-b", "a--b", "a---b", "a.b_c__d-e", "a0/b9",
		strings.Repeat("a", 255), strings.Repeat("a/", 127) + "a",
		"", "A", "Alpine", "team/Image", "a___b", "a..b", "a._b", "a-.b",
		".a", "a.", "_a", "a_", "-a", "a-", "/a", "a/", "a//b", "a/.b",
		"a b", "a:b", "a@b", "a%2fb", "a\\b",
	}
	for _, name := range names {
		if len(name) > 255 {
			t.Fatalf("%d characters: the table stays within the transport's cap", len(name))
		}
		_, transportErr := client.Image(name)
		_, _, _, parseErr := ParseRegistryPath(name + "/manifests/latest")
		if (transportErr == nil) != (parseErr == nil) {
			t.Errorf("%q: transport %v, parser %v", name, transportErr, parseErr)
		}
	}
}

package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

type names interface {
	Next(context.Context) (string, bool, error)
}

func drain(t *testing.T, src names) []string {
	t.Helper()
	var out []string
	for {
		name, ok, err := src.Next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, name)
	}
}

// pagedListings reads the fixture of listings a page at a time: one folder
// or manifest a query, so that every refill and every skip runs.
func pagedListings(t *testing.T, db item.Store) {
	ctx := t.Context()
	pages := db.Items().DockerPages()
	m := sum("m")
	tags := []string{"Alpha", "alpha", "list", "sha256__" + m, "v1", "v2"}
	for last, want := range map[string][]string{
		"": tags, "ALPHA": tags, "Alpha": tags[1:], "alpha": tags[2:], "B": tags[2:],
		"v2": nil, "~": nil,
	} {
		for _, batch := range []int{1, 2, 100} {
			got := drain(t, layout.TagsAfter(pages, "docker-local", "a_b.c", last, batch))
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("tags after %q, %d a query: %s", last, batch, diff)
			}
		}
	}
	for image, want := range map[string]bool{
		"a_b.c": true, "untagged": true, "absent": false, "plain": false, "bad-only": false,
	} {
		key := "docker-local"
		if image == "untagged" || image == "plain" {
			key = "other"
		}
		found, err := layout.TagsAfter(pages, key, image, "", 1).Found(ctx)
		if err != nil || found != want {
			t.Errorf("%s found %v, %v", image, found, err)
		}
	}
	// Found needs no query once a page has read a tag.
	read := layout.TagsAfter(pages, "docker-local", "a_b.c", "", 1)
	if _, ok, err := read.Next(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if found, err := read.Found(ctx); !found || err != nil {
		t.Fatal(found, err)
	}

	// The registry accepts lower-case names alone, whose byte order is
	// Compare's; with every name accepted the store's byte order shows.
	all := func(string) bool { return true }
	lower := func(s string) bool { return s == strings.ToLower(s) }
	local := []string{"a_b.c", "a_b.c/sub", "mixed"}
	for _, c := range []struct {
		key, prefix, last string
		accept            func(string) bool
		want              []string
	}{
		{"docker-local", "", "", all, []string{"aXb.c", "a_b.c", "a_b.c/sub", "mixed"}},
		{"docker-local", "", "", lower, local},
		{"docker-local", "", "A_B.C", lower, local},
		{"docker-local", "", "a_b.c", lower, local[1:]},
		{"docker-local", "", "A_B.C/SUB", lower, local[1:]},
		{"docker-local", "", "a_b.c/sub", lower, local[2:]},
		{"docker-local", "", "zzz", lower, nil},
		{
			"docker-local", "", "", func(s string) bool { return lower(s) && s != "a_b.c/sub" },
			[]string{"a_b.c", "mixed"},
		},
		{"other", "", "", lower, []string{"a_b.c", "untagged"}},
		{"empty", "", "", lower, nil},
		// Under a prefix the last compared is the prefixed name.
		{"other", "other/", "docker-local/zzz", lower, []string{"a_b.c", "untagged"}},
		{"other", "other/", "OTHER/A_B.C", lower, []string{"a_b.c", "untagged"}},
		{"other", "other/", "other/a_b.c", lower, []string{"untagged"}},
		{"other", "other/", "other", lower, []string{"a_b.c", "untagged"}},
		{"other", "other/", "zzz", lower, nil},
		{"other", "other/", "other0", lower, nil},
	} {
		for _, batch := range []int{1, 2, 100} {
			got := drain(t, layout.ImagesAfter(pages, c.key, c.prefix, c.last, c.accept, batch))
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("%s%s after %q, %d a query: %s", c.prefix, c.key, c.last, batch, diff)
			}
		}
	}
}

// failingPages fails every page read.
type failingPages struct{ port.ItemStore }

func (failingPages) DockerPages() port.DockerPageStore { return failingPages{} }

func (failingPages) ManifestFolders(context.Context, port.DockerFolderQuery) ([]string, error) {
	return nil, errors.New("boom")
}

func (failingPages) ImageManifests(
	context.Context, port.DockerImageQuery,
) ([]port.DockerImageManifest, error) {
	return nil, errors.New("boom")
}

func TestUnitPagedListingsFail(t *testing.T) {
	pages := failingPages{}.DockerPages()
	for name, src := range map[string]names{
		"tags":   layout.TagsAfter(pages, "r", "app", "", 1),
		"images": layout.ImagesAfter(pages, "r", "", "", func(string) bool { return true }, 1),
	} {
		_, _, err := src.Next(t.Context())
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := layout.TagsAfter(pages, "r", "app", "", 1).Found(t.Context()); err == nil {
		t.Error("found without a store")
	}
}

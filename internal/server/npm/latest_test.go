package npm_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format/formats"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// byPublish turns npm.tag_latest_by_publish on for the format the fixture
// writes through and for the protocol's reads, as the server's format
// settings reach both.
func (f *fixture) byPublish(t *testing.T) {
	t.Helper()
	s := npmfmt.Settings{TagLatestByPublish: true}
	f.items.Formats = formats.Settings{NPM: s}.For
	f.remount(t, npm.Options{Format: s})
}

// With npm.tag_latest_by_publish, npm publish gives latest to the version
// published last, pre-releases included, but not to one published with
// --tag; and a read that cannot see latest's version resolves it among the
// versions it can read by the same rule, where by default the highest
// would win.
func TestLatestByPublish(t *testing.T) {
	f := newFixture(t)
	f.byPublish(t)
	tags := func(user, name string) any {
		return decode(t, f.do("GET", registry+name, user))["dist-tags"]
	}
	for _, s := range []struct {
		version string
		tags    map[string]string
		want    map[string]any
	}{
		{"2.0.0", nil, map[string]any{"latest": "2.0.0"}},
		{"1.0.0", nil, map[string]any{"latest": "1.0.0"}},
		{"3.0.0-rc.1", nil, map[string]any{"latest": "3.0.0-rc.1"}},
		{
			"4.0.0",
			map[string]string{"next": "4.0.0"},
			map[string]any{"latest": "3.0.0-rc.1", "next": "4.0.0"},
		},
	} {
		wantStatus(t, f.publish(t, "pub", s.version, "writer", s.tags), http.StatusCreated)
		if diff := cmp.Diff(s.want, tags("reader", "pub")); diff != "" {
			t.Fatalf("after %s (-want +got):\n%s", s.version, diff)
		}
	}
	// partial reads mixed's 1.x only: latest's version is hidden from it.
	f.deploy(t, "mixed", "1.5.0", `{"name":"mixed","version":"1.5.0"}`, nil)
	f.deploy(t, "mixed", "1.2.0", `{"name":"mixed","version":"1.2.0"}`, nil)
	f.deploy(t, "mixed", "2.0.0", `{"name":"mixed","version":"2.0.0"}`, nil)
	if diff := cmp.Diff(map[string]any{"latest": "1.2.0"}, tags("partial", "mixed")); diff != "" {
		t.Fatalf("partial's dist-tags (-want +got):\n%s", diff)
	}
}

// A published version overwritten by a REST deploy with another version's
// content is no version any more, and takes its dist-tags with it: latest
// goes to the version that outranks the rest, and the file keeps none of
// them for a later conversion to hand on (#515).
func TestPublishedVersionOverwritten(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.publish(t, "over", "1.0.0", "writer", nil), http.StatusCreated)
	wantStatus(t, f.publish(t, "over", "2.0.0", "writer", nil), http.StatusCreated)
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	path := npmfmt.CanonicalPath("over", "2.0.0")
	it, err := f.db.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		t.Fatal(err)
	}
	// npm dist-tag add over@2.0.0 beta, as the property API sets it.
	if err := f.items.Annotate(ctx, c, it,
		types.Properties{npmfmt.PropDistTag: {"latest", "beta"}}, nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string]any{"latest": "2.0.0", "beta": "2.0.0"},
		decode(t, f.do("GET", registry+"over", "reader"))["dist-tags"]); diff != "" {
		t.Fatalf("published (-want +got):\n%s", diff)
	}
	if _, err := f.items.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path},
		Body:   bytes.NewReader(tarball(manifestOf("over", "0.5.0"))),
	}); err != nil {
		t.Fatal(err)
	}
	doc := decode(t, f.do("GET", registry+"over", "reader"))
	if diff := cmp.Diff(map[string]any{"latest": "1.0.0"}, doc["dist-tags"]); diff != "" {
		t.Fatalf("after the overwrite (-want +got):\n%s", diff)
	}
	if versions, _ := doc["versions"].(map[string]any); len(versions) != 1 {
		t.Fatalf("versions %v", versions)
	}
	if got := f.props(t, path)[npmfmt.PropDistTag]; got != nil {
		t.Fatalf("the overwritten tarball kept %v", got)
	}
}

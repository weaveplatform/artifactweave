package npm_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

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

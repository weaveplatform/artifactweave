package huggingface_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitDeleteRestoreAndReindex(t *testing.T) {
	f := newFixture(t, repo.Local)
	body := `{"key":"header","value":{"summary":"publish"}}` + "\n" + `{"key":"file","value":{"path":"config.json","content":"` + base64.StdEncoding.EncodeToString(
		[]byte(`{}`),
	) + `","encoding":"base64"}}` + "\n"
	r := f.request("POST", "api/models/acme/demo/commit/"+sha, "application/x-ndjson", []byte(body))
	b := readResponse(t, r)
	if r.StatusCode != 200 {
		t.Fatalf("commit %d %s", r.StatusCode, b)
	}
	c, e := f.db.Repos().Get(t.Context(), "hf")
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.db.Packages().Find(t.Context(), "hf", "models/acme/demo")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Packages().Version(t.Context(), p.ID, sha); e != nil {
		t.Fatal(e)
	}
	path := "models--acme--demo/snapshots/" + sha + "/config.json"
	if _, e := f.items.Delete(t.Context(), item.Target{Repo: c, Path: path}, "admin"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Packages().
		Find(t.Context(), "hf", "models/acme/demo"); !errors.Is(
		e,
		store.ErrNotFound,
	) {
		t.Fatalf("stale deleted index: %v", e)
	}
	trash, e := f.db.Trash().Find(t.Context(), "hf", path)
	if e != nil || len(trash) == 0 {
		t.Fatalf("trash: %v %v", e, trash)
	}
	if _, e := f.items.Restore(
		t.Context(),
		item.Target{Repo: c, Path: path},
		trash[0].Record,
		"admin",
	); e != nil {
		t.Fatal(e)
	}
	if e := (huggingface.Handler{}).Reindex(t.Context(), format.Env{Store: f.db}, c); e != nil {
		t.Fatal(e)
	}
	p, e = f.db.Packages().Find(t.Context(), "hf", "models/acme/demo")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Packages().Version(t.Context(), p.ID, sha); e != nil {
		t.Fatalf("missing restored version: %v", e)
	}
	r = f.request(http.MethodGet, "acme/demo/resolve/"+sha+"/config.json", "", nil)
	b = readResponse(t, r)
	if r.StatusCode != 200 || string(b) != "{}" {
		t.Fatalf("restored resolve %d %s", r.StatusCode, b)
	}
}

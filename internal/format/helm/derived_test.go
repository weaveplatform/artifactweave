package helm_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func indexQueue(t *testing.T, w *world) *work.Queue {
	t.Helper()
	q, err := work.New(w.st, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.items.Work = q
	if err := w.items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	return q
}

func derivedIndex(t *testing.T, w *world, c repo.Config) (helm.Index, []byte) {
	t.Helper()
	if _, err := w.items.Resolve(w.ctx, c, format.Download{Path: helm.IndexPath}); err != nil {
		t.Fatal(err)
	}
	it, err := w.st.Items().Get(w.ctx, c.Key, false, helm.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if it.CreatedBy != "_system_" || it.ModifiedBy != "_system_" ||
		it.MimeType != "application/x-yaml" {
		t.Fatalf("derived item %+v", it)
	}
	rc, _, err := w.items.Blobs.Open(w.ctx, it.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	var index helm.Index
	if err := yaml.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	return index, raw
}

func TestUnitDerivedIndex(t *testing.T) {
	w := newWorld(t)
	indexQueue(t, w)
	empty, raw := derivedIndex(t, w, w.c)
	if empty.APIVersion != "v1" || len(empty.Entries) != 0 ||
		!bytes.Contains(raw, []byte("entries: {}")) {
		t.Fatalf("empty index %s", raw)
	}
	var newest types.Item
	for i := range 10 {
		newest = w.deploy(
			fmt.Sprintf("charts/web-1.0.%d.tgz", i),
			chartTgz(
				"web",
				fmt.Sprintf("1.0.%d", i),
				"appVersion: 504e199",
				"description: 'quoted: text'",
				"dependencies:\n  - name: database\n    version: 1.0.0\n    repository: https://charts.example\n    alias: db",
			),
		)
	}
	stats, err := w.st.WorkQueue().Stats(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pending int64
	for _, s := range stats {
		if s.Kind == helm.KindIndex {
			pending += s.Pending
		}
	}
	if pending != 1 {
		t.Fatalf("ten deploys queued %d indexes", pending)
	}
	index, raw := derivedIndex(t, w, w.c)
	entries := index.Entries["web"]
	if len(entries) != 10 || entries[0].Version != "1.0.9" ||
		entries[0].Digest != newest.Actual.SHA256 ||
		entries[0].Created != newest.Modified.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("entries %+v", entries)
	}
	if entries[0].Dependencies[0].Alias != "db" ||
		!bytes.Contains(raw, []byte(`version: "1.0.9"`)) ||
		!bytes.Contains(raw, []byte(`appVersion: "504e199"`)) {
		t.Fatalf("quoted fields and alias: %s", raw)
	}
	w.remove("charts/web-1.0.9.tgz")
	index, _ = derivedIndex(t, w, w.c)
	if len(index.Entries["web"]) != 9 || index.Entries["web"][0].Version != "1.0.8" {
		t.Fatal("delete left stale index")
	}
	w.deploy("index.yaml", []byte("entries: {bogus: []}\n"))
	index, _ = derivedIndex(t, w, w.c)
	if _, ok := index.Entries["bogus"]; ok {
		t.Fatal("client index was not replaced")
	}
	w.remove("index.yaml")
	_, _ = derivedIndex(t, w, w.c)
	w.deploy("sub/index.yaml", []byte("ordinary"))
	if _, err := w.st.WorkQueue().
		Get(w.ctx, helm.KindIndex, w.c.Key); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("nested index queued work: %v", err)
	}
}

func TestUnitDerivedIndexTransferAndProperties(t *testing.T) {
	w := newWorld(t)
	indexQueue(t, w)
	other := w.repo("other", nil)
	w.deploy("web-1.0.0.tgz", chartTgz("web", "1.0.0"))
	_, _ = derivedIndex(t, w, w.c)
	w.transfer(w.c, "web-1.0.0.tgz", other, "space dir/a:b.tgz", false)
	index, _ := derivedIndex(t, w, other)
	if index.Entries["web"][0].URLs[0] != "space%20dir/a:b.tgz" {
		t.Fatalf("URL %+v", index.Entries)
	}
	w.transfer(w.c, "web-1.0.0.tgz", other, "moved.tgz", true)
	index, _ = derivedIndex(t, w, w.c)
	if len(index.Entries) != 0 {
		t.Fatal("move source retained chart")
	}
	it, err := w.st.Items().Get(w.ctx, other.Key, false, "moved.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.items.Annotate(
		w.ctx,
		other,
		it,
		types.Properties{"chart.appVersion": {"edited"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	_, _ = derivedIndex(t, w, other)
	// A property edit after the queued move is fresh queues nothing.
	if err := w.items.Annotate(
		w.ctx,
		other,
		it,
		types.Properties{"chart.version": {"other"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.WorkQueue().
		Get(w.ctx, helm.KindIndex, other.Key); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("annotation queued index: %v", err)
	}
}

func TestUnitHelmIndexEncodingAndSettings(t *testing.T) {
	on, off := true, false
	if !(helm.Settings{}).Relative() || !(helm.Settings{RelativeURLs: &on}).Relative() ||
		(helm.Settings{RelativeURLs: &off}).Relative() {
		t.Fatal("URL defaults")
	}
	h := helm.Handler{}
	if h.Classify(repo.Config{}, "index.yaml") != format.ClassDerived ||
		h.Classify(repo.Config{}, "nested/index.yaml") != format.ClassUnique {
		t.Fatal("classification")
	}
	w := newWorld(t)
	indexQueue(t, w)
	w.deploy("a:b.tgz", chartTgz("web", "not-semver"))
	w.deploy("good.tgz", chartTgz("web", "1.0.0"))
	out, _ := derivedIndex(t, w, w.c)
	if out.Entries["web"][0].Version != "1.0.0" || out.Entries["web"][1].URLs[0] != "a%3Ab.tgz" {
		t.Fatalf("order or URL: %+v", out.Entries)
	}
	for _, c := range []repo.Config{{Key: "absent"}, w.repo("generic", func(c *repo.Config) { c.PackageType = repo.Generic }), w.repo("remote", func(c *repo.Config) { c.Class = repo.Remote; c.URL = "https://charts.example" })} {
		if err := h.Work(
			context.Background(),
			w.items.Env(),
			types.WorkItem{Kind: helm.KindIndex, Key: c.Key},
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := helm.EncodeIndex(
		helm.Index{
			Entries: map[string][]helm.ChartVersion{
				"x": {
					{
						Chart: helm.Chart{
							Dependencies: []helm.Dependency{{ImportValues: []any{func() {}}}},
						},
					},
				},
			},
		},
	); err == nil ||
		!strings.Contains(err.Error(), "encode index") {
		t.Fatalf("bad imported value: %v", err)
	}
}

func TestUnitIndexWorkerFaults(t *testing.T) {
	for _, method := range []string{"List", "Versions"} {
		t.Run(method, func(t *testing.T) {
			f := &faults{}
			w := newWorldOn(t, &faulty{Store: inmem.New(), f: f})
			w.deploy("web.tgz", chartTgz("web", "1.0.0"))
			f.method = method
			if err := (helm.Handler{}).Work(
				w.ctx,
				w.items.Env(),
				types.WorkItem{Kind: helm.KindIndex, Key: w.c.Key},
			); !errors.Is(
				err,
				errFault,
			) {
				t.Fatalf("%s: %v", method, err)
			}
		})
	}
	w := newWorld(t)
	w.deploy("web.tgz", chartTgz("web", "1.0.0"))
	env := w.items.Env()
	env.Derive = func(context.Context, format.Derived) (types.Item, error) { return types.Item{}, errFault }
	if err := (helm.Handler{}).Work(
		w.ctx,
		env,
		types.WorkItem{Kind: helm.KindIndex, Key: w.c.Key},
	); !errors.Is(
		err,
		errFault,
	) {
		t.Fatalf("derive: %v", err)
	}
}

func TestUnitColdIndexPreservesParkedWork(t *testing.T) {
	w := newWorld(t)
	indexQueue(t, w)
	if err := w.st.WorkQueue().Enqueue(w.ctx, helm.KindIndex, w.c.Key, nil, w.now); err != nil {
		t.Fatal(err)
	}
	workItem, leased, err := w.st.WorkQueue().
		LeaseKey(w.ctx, helm.KindIndex, w.c.Key, store.WorkLease{Holder: "test", Now: w.now, Until: w.now.Add(time.Minute)})
	if err != nil || !leased {
		t.Fatalf("lease: %v %v", leased, err)
	}
	if err := w.st.WorkQueue().
		Fail(w.ctx, helm.KindIndex, w.c.Key, workItem.Generation, store.WorkFailure{Park: true, Now: w.now, Error: "broken index"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.items.Resolve(w.ctx, w.c, format.Download{Path: helm.IndexPath}); err == nil {
		t.Fatal("cold read reset parked work")
	}
	pending, err := w.st.WorkQueue().Get(w.ctx, helm.KindIndex, w.c.Key)
	if err != nil || pending.State != types.WorkParked {
		t.Fatalf("parked item %+v %v", pending, err)
	}
	for _, download := range []format.Download{{Path: "other.yaml"}, {Path: helm.IndexPath, Cached: true}} {
		got, err := (helm.Handler{}).Resolve(w.ctx, w.items.Env(), w.c, download)
		if err != nil || got != download.Path {
			t.Fatalf("unrelated resolve %q %v", got, err)
		}
	}
}

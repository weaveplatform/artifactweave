package nuget_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func pdbPackage(id string, files ...zipFile) []byte {
	return zipOf(
		append(
			[]zipFile{
				{
					id + ".nuspec",
					nuspecXML(
						id,
						"1.0.0",
						`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`,
					),
				},
			},
			files...)...)
}

func (w *world) content(p string) []byte {
	w.t.Helper()
	w.freshSymbols()
	it := w.item(p)
	r, _, err := w.items.Blobs.Open(w.ctx, it.Actual.SHA256)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		w.t.Fatal(err)
	}
	return b
}

func (w *world) freshSymbols() {
	w.t.Helper()
	if err := w.q.EnsureFresh(w.ctx, nuget.KindSymbols, w.c.Key); err != nil {
		w.t.Fatal(err)
	}
}

func TestUnitSymbolLifecycleAndOwners(t *testing.T) {
	w := newWorld(t, nil)
	portable, windows := portablePDB(), windowsPDB()
	p := ".symbols/app.pdb/" + symbolGUID + "FFFFFFFF/app.pdb"
	win := ".symbols/native.pdb/" + symbolGUID + "2A/native.pdb"
	// Source path wins even when deployed later, with different bytes of
	// the same identity; removing it must fall back to the surviving owner.
	b := append([]byte(nil), portable...)
	b[len(b)-1] = 1
	w.deploy("B.1.0.0.snupkg", pdbPackage("B", zipFile{"lib/App.pdb", string(b)}), nil)
	w.deploy(
		"A.1.0.0.snupkg",
		pdbPackage(
			"A",
			zipFile{"lib/App.pdb", string(portable)},
			zipFile{"native/Native.pdb", string(windows)},
		),
		nil,
	)
	if !bytes.Equal(w.content(p), portable) || !bytes.Equal(w.content(win), windows) {
		t.Fatal("derived content")
	}
	v := w.version("a", "1.0.0")
	if len(v.Files) != 1 || len(v.Metadata.SymbolSources) != 1 ||
		len(v.Metadata.SymbolSources[0].Files) != 2 {
		t.Fatalf("index %+v", v)
	}
	// Editing public properties cannot remove trusted owners.
	if err := w.items.Annotate(
		w.ctx,
		w.c,
		w.item(p),
		map[string][]string{"nuget.symbol.owner": {"fake"}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	w.remove("A.1.0.0.snupkg")
	if !bytes.Equal(w.content(p), b) || w.stored(win) {
		t.Fatal("shared owner deletion")
	}
	w.restore("A.1.0.0.snupkg")
	if !bytes.Equal(w.content(p), portable) || !w.stored(win) {
		t.Fatal("restore derivation")
	}
	w.remove(p)
	if w.stored(p) {
		t.Fatal("derived delete")
	}
	w.reindex()
	if !bytes.Equal(w.content(p), portable) {
		t.Fatal("reindex recovery")
	}
	w.remove("A.1.0.0.snupkg")
	w.remove("B.1.0.0.snupkg")
	w.freshSymbols()
	if w.stored(p) || w.stored(win) {
		t.Fatal("orphan symbols")
	}
	w.reindex()
	if w.stored(p) {
		t.Fatal("reindex resurrected symbol")
	}
}

func TestUnitSymbolOverwriteCopyAndReindex(t *testing.T) {
	w := newWorld(t, nil)
	body := pdbPackage("A", zipFile{"lib/A.pdb", string(portablePDB())})
	p := ".symbols/a.pdb/" + symbolGUID + "FFFFFFFF/a.pdb"
	w.deploy("A.1.0.0.snupkg", body, nil)
	// Normal package deployment preserves the manifest without reopening
	// every other archive; properties remain unrelated to symbol identity.
	w.deploy("A.1.0.0.nupkg", pkg("A", "1.0.0", ""), nil)
	if len(w.version("a", "1.0.0").Metadata.SymbolSources) != 1 {
		t.Fatal("manifest lost")
	}
	to := w.repo("copy", repo.Local, nil)
	if failed := w.transfer(w.c, "A.1.0.0.snupkg", to, "A.1.0.0.snupkg", false); len(failed) > 0 {
		t.Fatal(failed)
	}
	if err := w.q.EnsureFresh(w.ctx, nuget.KindSymbols, to.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.Items().Get(w.ctx, to.Key, false, p); err != nil {
		t.Fatal("copy symbol", err)
	}
	w.deploy("A.1.0.0.snupkg", pdbPackage("A", zipFile{"lib/B.pdb", string(windowsPDB())}), nil)
	w.freshSymbols()
	if w.stored(p) {
		t.Fatal("old symbol retained after overwrite")
	}
	w.reindex()
	if w.stored(p) {
		t.Fatal("old symbol reindexed")
	}
	if _, err := w.st.Items().Get(w.ctx, to.Key, false, p); err != nil {
		t.Fatal("copy isolation", err)
	}
	for _, name := range []string{p, ".symbols/native.pdb/x/native.pdb"} {
		if (nuget.Handler{}).Classify(w.c, name) != format.ClassDerived {
			t.Fatal(name)
		}
	}
	if (nuget.Handler{}).Classify(w.c, "A.1.0.0.snupkg") != format.ClassUnique {
		t.Fatal("source replication")
	}
}

func TestUnitSymbolArchiveDuplicateIdentityAndFailures(t *testing.T) {
	b := portablePDB()
	changed := append([]byte(nil), b...)
	changed[len(changed)-1] = 1
	for _, order := range [][]zipFile{
		{{"lib/a.pdb", string(b)}, {"ref/a.pdb", string(changed)}},
		{{"ref/a.pdb", string(changed)}, {"lib/a.pdb", string(b)}},
	} {
		w := newWorld(t, nil)
		w.deploy("A.1.0.0.snupkg", pdbPackage("A", order...), nil)
		if !bytes.Equal(w.content(".symbols/a.pdb/"+symbolGUID+"FFFFFFFF/a.pdb"), b) {
			t.Fatal("entry order changed winner")
		}
	}
	w := newWorld(t, nil)
	// A PDB decompression limit is enforced before a deploy commits.
	_, err := w.tryDeploy(
		w.c,
		"Huge.1.0.0.snupkg",
		pdbPackage("Huge", zipFile{"lib/a.pdb", strings.Repeat("x", 64<<20+1)}),
		nil,
	)
	var reject *format.RejectionError
	if !errors.As(err, &reject) || w.stored("Huge.1.0.0.snupkg") {
		t.Fatalf("oversized archive %v", err)
	}
}

func TestUnitSymbolWorkFaultsAndObsoleteRepositories(t *testing.T) {
	w := newWorld(t, nil)
	w.deploy("A.1.0.0.snupkg", pdbPackage("A", zipFile{"lib/app.pdb", string(portablePDB())}), nil)
	run := func(env format.Env, key string) error {
		return (nuget.Handler{}).Work(w.ctx, env, types.WorkItem{Kind: nuget.KindSymbols, Key: key})
	}
	for _, method := range []string{"RepoGet", "Versions", "Search"} {
		env := w.items.Env()
		env.Store = &faulty{Store: w.st, f: &faults{pass: map[string]int{method: 0}}}
		if err := run(env, w.c.Key); !errors.Is(err, errFault) {
			t.Fatalf("%s: %v", method, err)
		}
	}
	env := w.items.Env()
	env.Derive = func(context.Context, format.Derived) (types.Item, error) { return types.Item{}, errFault }
	if err := run(env, w.c.Key); !errors.Is(err, errFault) {
		t.Fatal("derive", err)
	}
	if w.stored(".symbols/app.pdb/" + symbolGUID + "FFFFFFFF/app.pdb") {
		t.Fatal("failed work committed")
	}
	w.freshSymbols()
	w.remove("A.1.0.0.snupkg")
	env = w.items.Env()
	env.Underive = func(context.Context, repo.Config, bool, string) error { return errFault }
	if err := run(env, w.c.Key); !errors.Is(err, errFault) {
		t.Fatal("underive", err)
	}
	w.freshSymbols()
	remote := w.repo("nuget-remote", repo.Remote, nil)
	for _, key := range []string{"missing", remote.Key} {
		if err := run(w.items.Env(), key); err != nil {
			t.Fatal(err)
		}
	}
	c := w.c
	c.PackageType = repo.Generic
	if err := w.st.Repos().Update(w.ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := run(w.items.Env(), c.Key); err != nil {
		t.Fatal(err)
	}
	if refs := (nuget.Handler{}).Owed(
		w.c,
		false,
		".symbols/a.pdb/key/a.pdb",
	); len(refs) != 1 ||
		refs[0].Kind != nuget.KindSymbols {
		t.Fatal(refs)
	}
	if refs := (nuget.Handler{}).Owed(w.c, true, ".symbols/a.pdb/key/a.pdb"); len(refs) != 0 {
		t.Fatal(refs)
	}
}

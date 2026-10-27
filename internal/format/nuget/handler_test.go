package nuget_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// rich is the metadata of a package as dotnet pack writes it.
const rich = `<title>Contoso Utility</title><authors>Ana, Ben</authors><owners>contoso</owners>` +
	`<requireLicenseAcceptance>false</requireLicenseAcceptance>` +
	`<license type="expression">MIT</license><licenseUrl>https://licenses.nuget.org/MIT</licenseUrl>` +
	`<projectUrl>https://contoso.example</projectUrl><iconUrl>https://contoso.example/i.png</iconUrl>` +
	`<readme>README.md</readme><description>Helpers &amp; more.</description><summary>S</summary>` +
	`<releaseNotes>R</releaseNotes><copyright>(c) Contoso</copyright><language>en</language>` +
	`<tags>util helpers</tags>` +
	`<packageTypes><packageType name="Dependency"/></packageTypes>` +
	`<dependencies><group targetFramework="net8.0">` +
	`<dependency id="Newtonsoft.Json" version="[13.0.1, )"/><dependency id="Polly" version="8.0"/>` +
	`</group><group targetFramework=".NETStandard2.0"><dependency id="Polly" version="8.0"/></group>` +
	`</dependencies>`

// A package deployed at its canonical name is indexed under its id
// lower-cased and its version normalised and lower-cased, with Metadata
// that describes it whole, and carries the nuget.* properties from its
// nuspec beside a user's own. The version is visible at once.
func TestUnitDeployIndexes(t *testing.T) {
	w := newWorld(t, nil)
	body := pkg("Contoso.Utility", "1.2.0.0-Beta+abc", rich)
	p := "Contoso.Utility.1.2.0-Beta.nupkg"
	it := w.deploy(p, body, types.Properties{"build.name": {"ci"}})
	sum := sha512.Sum512(body)
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	v := w.version("contoso.utility", "1.2.0-beta")
	want := nuget.Metadata{
		ID: "Contoso.Utility", Version: "1.2.0-Beta", OriginalVersion: "1.2.0.0-Beta+abc",
		Title: "Contoso Utility", Authors: "Ana, Ben", Owners: "contoso",
		Description: "Helpers & more.", Summary: "S", ReleaseNotes: "R", Copyright: "(c) Contoso",
		Language: "en", Tags: "util helpers", ProjectURL: "https://contoso.example",
		IconURL: "https://contoso.example/i.png", LicenseURL: "https://licenses.nuget.org/MIT",
		LicenseExpression: "MIT", Readme: "README.md",
		DependencyGroups: []nuget.DependencyGroup{
			{TargetFramework: "net8.0", Dependencies: []nuget.Dependency{
				{ID: "Newtonsoft.Json", Range: "[13.0.1, )"}, {ID: "Polly", Range: "8.0"},
			}},
			{TargetFramework: ".NETStandard2.0", Dependencies: []nuget.Dependency{
				{ID: "Polly", Range: "8.0"},
			}},
		},
		PackageTypes: []nuget.PackageType{{Name: "Dependency"}},
		Nuspec:       nuspecXML("Contoso.Utility", "1.2.0.0-Beta+abc", rich),
		Path:         p, SHA512: b64, Size: int64(len(body)), SemVer2: true,
		Published: it.Modified.UTC(),
	}
	if diff := cmp.Diff(want, v.Metadata); diff != "" {
		t.Fatal(diff)
	}
	if !v.Created.Equal(it.Modified) || !slices.Equal(v.Files, []string{p}) {
		t.Fatalf("%+v", v)
	}
	if !v.Metadata.HasPackage() || !v.Metadata.IsPrerelease() {
		t.Fatal("the version reads as no pre-release package")
	}
	wantProps := types.Properties{
		"build.name":                       {"ci"},
		nuget.PropID:                       {"Contoso.Utility"},
		nuget.PropVersion:                  {"1.2.0-Beta"},
		nuget.PropTitle:                    {"Contoso Utility"},
		nuget.PropAuthors:                  {"Ana, Ben"},
		nuget.PropOwners:                   {"contoso"},
		nuget.PropSummary:                  {"S"},
		nuget.PropDescription:              {"Helpers & more."},
		nuget.PropCopyright:                {"(c) Contoso"},
		nuget.PropReleaseNotes:             {"R"},
		nuget.PropProjectURL:               {"https://contoso.example"},
		nuget.PropLicenseURL:               {"https://licenses.nuget.org/MIT"},
		nuget.PropIconURL:                  {"https://contoso.example/i.png"},
		nuget.PropTags:                     {"util helpers"},
		nuget.PropRequireLicenseAcceptance: {"false"},
		nuget.PropDigest:                   {b64},
		nuget.PropDependency: {
			"Newtonsoft.Json:[13.0.1, ):net8.0", "Polly:8.0:.NETStandard2.0", "Polly:8.0:net8.0",
		},
	}
	got := w.props(p)
	for k, v := range got {
		got[k] = sortedStrings(v)
	}
	if diff := cmp.Diff(wantProps, got); diff != "" {
		t.Fatal(diff)
	}
}

// A file name is canonical when it is <id>.<normalised version>.nupkg
// compared ignoring case, in any folder; anything else that is a .nupkg or
// .snupkg is refused with 409, and so is one that is no valid package.
// Files of other names, legacy symbol packages among them, are stored
// whatever they hold and never indexed.
func TestUnitCanonicalNames(t *testing.T) {
	w := newWorld(t, nil)
	w.deploy("foo.1.0.0-BETA.nupkg", pkg("Foo", "1.0.0-beta", ""), nil)
	w.deploy("deep/er/Bar.2.0.0.nupkg", pkg("Bar", "02.0", ""), nil)
	w.deploy("Foo.1.0.0-beta.symbols.nupkg", []byte("anything"), nil)
	w.deploy("Foo.1.0.0-beta.NUPKG", []byte("anything"), nil)
	w.deploy("Foo.nuspec", []byte("<package/>"), nil)
	if diff := cmp.Diff(
		map[string][]string{"foo": {"1.0.0-beta"}, "bar": {"2.0.0"}},
		map[string][]string{
			"foo": w.versions("foo"), "bar": w.versions("bar"),
		},
	); diff != "" {
		t.Fatal(diff)
	}
	for _, c := range []struct {
		path string
		body []byte
		want string
	}{
		{
			"Baz.1.0.nupkg", pkg("Baz", "1.0", ""),
			"The repository 'nuget-local' rejected 'Baz.1.0.nupkg': the file name of Baz 1.0.0 " +
				"must be Baz.1.0.0.nupkg.",
		},
		{"Baz.1.0.0.0.nupkg", pkg("Baz", "1.0", ""), "must be Baz.1.0.0.nupkg"},
		{"Baz.1.0.0+meta.nupkg", pkg("Baz", "1.0.0+meta", ""), "must be Baz.1.0.0.nupkg"},
		{"Other.1.0.0.nupkg", pkg("Baz", "1.0.0", ""), "must be Baz.1.0.0.nupkg"},
		{"Baz.1.0.0.snupkg", pkg("Baz", "1.0.1", ""), "must be Baz.1.0.1.snupkg"},
		{
			"Baz.1.0.0.nupkg", []byte("not a package"),
			"The repository 'nuget-local' rejected 'Baz.1.0.0.nupkg': not a valid NuGet package: " +
				"it is no zip.",
		},
		{"Baz.1.0.0.snupkg", pkgWith(), "no .nuspec at its root"},
	} {
		_, err := w.tryDeploy(w.c, c.path, c.body, nil)
		var r *format.RejectionError
		if !errors.As(err, &r) || r.Status != http.StatusConflict ||
			!strings.Contains(r.Message, c.want) {
			t.Errorf("%s: %v", c.path, err)
		}
		if w.stored(c.path) {
			t.Errorf("%s was stored", c.path)
		}
	}
}

// refusedWith checks err is a 409 with message.
func refusedWith(t *testing.T, err error, message string) {
	t.Helper()
	var r *format.RejectionError
	if !errors.As(err, &r) || r.Status != http.StatusConflict || r.Message != message {
		t.Fatalf("%v", err)
	}
}

// refusedTransfer checks a copy or move report names the duplicate.
func refusedTransfer(t *testing.T, msgs []string) {
	t.Helper()
	if !slices.ContainsFunc(msgs, func(m string) bool {
		return strings.HasSuffix(
			m,
			": The repository 'nuget-local' already contains Foo 1.0.0-Beta",
		)
	}) {
		t.Fatalf("%v", msgs)
	}
}

// A version holds one package and one set of symbols whichever route
// brings another: a REST deploy elsewhere, a spelling NuGet takes for the
// same version, a checksum deploy, an archive's entry, a copy and a move
// from another repository all answer 409. A write at the version's own
// path is an overwrite, and a move of the version's own file within the
// repository takes the version along.
func TestUnitDuplicates(t *testing.T) {
	w := newWorld(t, nil)
	body := pkg("Foo", "1.0.0-Beta", "")
	w.deploy("Foo.1.0.0-Beta.nupkg", body, nil)
	w.deploy("Foo.1.0.0-Beta.snupkg", symbols("Foo", "1.0.0-Beta"), nil)
	dup := "The repository 'nuget-local' already contains Foo 1.0.0-beta"
	for _, c := range []struct {
		path string
		body []byte
		want string
	}{
		{"team/Foo.1.0.0-Beta.nupkg", body, "The repository 'nuget-local' already contains Foo 1.0.0-Beta"},
		{"team/Foo.1.0.0-beta.nupkg", pkg("Foo", "1.00.0-beta", ""), dup},
		{
			"team/foo.1.0.0-beta.nupkg", pkg("foo", "1.0.0.0-beta+x", ""),
			"The repository 'nuget-local' already contains foo 1.0.0-beta",
		},
		{
			"x/Foo.1.0.0-beta.snupkg", symbols("Foo", "1.0.0-beta"),
			"The repository 'nuget-local' already contains the symbols of Foo 1.0.0-beta",
		},
	} {
		_, err := w.tryDeploy(w.c, c.path, c.body, nil)
		refusedWith(t, err, c.want)
	}
	// A checksum deploy of the same content elsewhere.
	src := w.item("Foo.1.0.0-Beta.nupkg")
	_, err := w.items.DeployByChecksum(w.ctx, item.Deploy{
		Target:   item.Target{Repo: w.c, Path: "copy/Foo.1.0.0-Beta.nupkg"},
		Declared: types.Checksums{SHA1: src.Actual.SHA1}, By: "tester",
	})
	refusedWith(t, err, "The repository 'nuget-local' already contains Foo 1.0.0-Beta")
	// An archive's entry.
	_, err = w.items.Explode(w.ctx, item.Explode{
		Deploy: item.Deploy{
			Target: item.Target{Repo: w.c, Path: "bundle/x.zip"},
			Body:   bytes.NewReader(zipOf(zipFile{"Foo.1.0.0-Beta.nupkg", string(body)})),
		},
		Atomic: true, CanOverwrite: true,
	})
	refusedWith(t, err, "The repository 'nuget-local' already contains Foo 1.0.0-Beta")
	// A copy within the repository, and a copy and a move from another.
	refusedTransfer(
		t,
		w.transfer(w.c, "Foo.1.0.0-Beta.nupkg", w.c, "dup/Foo.1.0.0-Beta.nupkg", false),
	)
	other := w.repo("nuget-other", repo.Local, nil)
	if _, err := w.tryDeploy(other, "Foo.1.0.0-Beta.nupkg", body, nil); err != nil {
		t.Fatal(err)
	}
	for _, move := range []bool{false, true} {
		refusedTransfer(
			t,
			w.transfer(other, "Foo.1.0.0-Beta.nupkg", w.c, "o/Foo.1.0.0-Beta.nupkg", move),
		)
	}
	// A move of the version's own files within the repository.
	if msgs := w.transfer(
		w.c,
		"Foo.1.0.0-Beta.nupkg",
		w.c,
		"moved/Foo.1.0.0-Beta.nupkg",
		true,
	); msgs != nil {
		t.Fatalf("own move: %v", msgs)
	}
	if msgs := w.transfer(
		w.c,
		"Foo.1.0.0-Beta.snupkg",
		w.c,
		"moved/Foo.1.0.0-Beta.snupkg",
		true,
	); msgs != nil {
		t.Fatalf("own move: %v", msgs)
	}
	v := w.version("foo", "1.0.0-beta")
	if diff := cmp.Diff(
		[]string{"moved/Foo.1.0.0-Beta.nupkg", "moved/Foo.1.0.0-Beta.snupkg"},
		v.Files,
	); diff != "" {
		t.Fatal(diff)
	}
	if v.Metadata.Path != "moved/Foo.1.0.0-Beta.nupkg" ||
		v.Metadata.Symbols != "moved/Foo.1.0.0-Beta.snupkg" {
		t.Fatalf("%+v", v.Metadata)
	}
}

// A write at the version's own path is an overwrite: the new content is
// indexed, and its nuget.* properties are those of its nuspec alone.
func TestUnitOverwrite(t *testing.T) {
	w := newWorld(t, nil)
	p := "Foo.1.0.0.nupkg"
	w.deploy(p, pkg("Foo", "1.0.0", "<title>Old</title><tags>a b</tags>"), nil)
	if got := w.props(p)[nuget.PropTitle]; !slices.Equal(got, []string{"Old"}) {
		t.Fatal(got)
	}
	w.deploy(p, pkg("Foo", "1.0.0.0", "<summary>New</summary>"), nil)
	v := w.version("foo", "1.0.0")
	if v.Metadata.Title != "" || v.Metadata.Summary != "New" ||
		v.Metadata.OriginalVersion != "1.0.0.0" {
		t.Fatalf("%+v", v.Metadata)
	}
	props := w.props(p)
	if _, ok := props[nuget.PropTitle]; ok || props[nuget.PropSummary][0] != "New" ||
		props[nuget.PropTags] != nil {
		t.Fatalf("%v", props)
	}
	// Garbage over a package is refused, and the package stays as it was.
	if _, err := w.tryDeploy(w.c, p, []byte("garbage"), nil); err == nil {
		t.Fatal("garbage stored over a package")
	}
	if w.version("foo", "1.0.0").Metadata.Summary != "New" {
		t.Fatal("the refused write changed the index")
	}
}

// Symbols are the second file of their version, wherever each is stored:
// pushed first, they make a version with no package, which the feeds do
// not show; the package then joins them and the Metadata describes it;
// and each going leaves the other.
func TestUnitSymbolsLinking(t *testing.T) {
	w := newWorld(t, nil)
	sym := w.deploy("Foo.2.0.0.snupkg", symbols("Foo", "2.0"), nil)
	v := w.version("foo", "2.0.0")
	if v.Metadata.HasPackage() || v.Metadata.Symbols != "Foo.2.0.0.snupkg" ||
		!v.Created.Equal(sym.Modified) || v.Metadata.ID != "" {
		t.Fatalf("%+v", v)
	}
	if _, ok := w.props("Foo.2.0.0.snupkg")[nuget.PropID]; ok {
		t.Fatal("symbols got the package's properties")
	}
	pk := w.deploy("lib/Foo.2.0.0.nupkg", pkg("Foo", "2.0.0", "<title>T</title>"), nil)
	v = w.version("foo", "2.0.0")
	if v.Metadata.Path != "lib/Foo.2.0.0.nupkg" || v.Metadata.Symbols != "Foo.2.0.0.snupkg" ||
		v.Metadata.Title != "T" || !v.Created.Equal(pk.Modified) || len(v.Files) != 2 {
		t.Fatalf("%+v", v)
	}
	// Symbols pushed again over themselves keep the package's description.
	w.deploy("Foo.2.0.0.snupkg", symbols("Foo", "2.0.0"), nil)
	if v := w.version("foo", "2.0.0"); v.Metadata.Title != "T" || len(v.Files) != 2 {
		t.Fatalf("%+v", v)
	}
	w.remove("lib/Foo.2.0.0.nupkg")
	v = w.version("foo", "2.0.0")
	if v.Metadata.HasPackage() || v.Metadata.Symbols != "Foo.2.0.0.snupkg" || len(v.Files) != 1 {
		t.Fatalf("%+v", v)
	}
	w.deploy("Foo.2.0.0.nupkg", pkg("Foo", "2.0.0", ""), nil)
	w.remove("Foo.2.0.0.snupkg")
	v = w.version("foo", "2.0.0")
	if !v.Metadata.HasPackage() || v.Metadata.Symbols != "" || len(v.Files) != 1 {
		t.Fatalf("%+v", v)
	}
	w.remove("Foo.2.0.0.nupkg")
	if got := w.index(w.c); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// A file that takes content which no longer makes it canonical leaves the
// version it was a file of, which goes with its last file. Only a remote
// repository's cache, which no rule refuses, can store such content over
// a package.
func TestUnitFileLeavesItsVersion(t *testing.T) {
	w := newWorld(t, nil)
	r := w.repo("nuget-remote", repo.Remote, nil)
	cache := func(p string, body []byte) {
		t.Helper()
		w.now = w.now.Add(time.Minute)
		if _, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: r, Cached: true, Path: p}, Body: bytes.NewReader(body),
		}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	cache("Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", ""))
	cache("Foo.1.0.0.snupkg", symbols("Foo", "1.0.0"))
	cache("Foo.3.0.0.nupkg", pkg("Foo", "3.0.0", ""))
	cache("Foo.1.0.0.nupkg", pkg("Foo", "2.0.0", ""))
	v := w.index(r)["foo"]["1.0.0"]
	if v.Metadata.HasPackage() || !slices.Equal(v.Files, []string{"Foo.1.0.0.snupkg"}) {
		t.Fatalf("%+v", v)
	}
	cache("Foo.1.0.0.snupkg", []byte("garbage"))
	if got := w.index(r); len(got["foo"]) != 1 {
		t.Fatalf("%v", got)
	}
	cache("Foo.3.0.0.nupkg", []byte("garbage"))
	if got := w.index(r); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// A restore brings back what no write could: a second package of a
// version. The one deployed first is the one the Metadata describes, and
// when it goes the other takes its place, as a reindex would read it.
func TestUnitRestoredSecondPackage(t *testing.T) {
	w := newWorld(t, nil)
	w.deploy("Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", "<title>First</title>"), nil)
	w.remove("Foo.1.0.0.nupkg")
	w.deploy("b/Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", "<title>Second</title>"), nil)
	w.restore("Foo.1.0.0.nupkg")
	v := w.version("foo", "1.0.0")
	if len(v.Files) != 2 || v.Metadata.Title != "First" || v.Metadata.Path != "Foo.1.0.0.nupkg" {
		t.Fatalf("%+v", v)
	}
	before := w.index(w.c)
	w.reindex()
	if diff := cmp.Diff(before, w.index(w.c)); diff != "" {
		t.Fatal(diff)
	}
	w.remove("Foo.1.0.0.nupkg")
	v = w.version("foo", "1.0.0")
	if len(v.Files) != 1 || v.Metadata.Title != "Second" || v.Metadata.Path != "b/Foo.1.0.0.nupkg" {
		t.Fatalf("%+v", v)
	}
}

// A reindex rebuilds the index from the packages, and writes back the
// nuget.* properties a user changed or removed, removes one the nuspec
// does not give, and leaves the user's own alone.
func TestUnitReindex(t *testing.T) {
	w := newWorld(t, nil)
	w.deploy("Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", "<title>T</title>"), nil)
	w.deploy("Foo.1.0.0.snupkg", symbols("Foo", "1.0.0"), nil)
	// A lower version deployed later: the package is dated by its first.
	w.deploy("Foo.0.9.0.nupkg", pkg("Foo", "0.9", ""), nil)
	w.deploy("x/Bar.2.0.0-rc.1.nupkg", pkg("Bar", "2.0.0-rc.1", ""), nil)
	w.deploy("Bar.2.0.0-rc.1.symbols.nupkg", pkg("Bar", "2.0.0-rc.1", ""), nil)
	kept := w.index(w.c)
	it := w.item("Foo.1.0.0.nupkg")
	if err := w.items.Annotate(w.ctx, w.c, it,
		types.Properties{nuget.PropID: {"Spoof"}, nuget.PropSummary: {"user"}, "keep": {"me"}},
		[]string{nuget.PropTitle}); err != nil {
		t.Fatal(err)
	}
	if w.version("foo", "1.0.0").Metadata.ID != "Foo" {
		t.Fatal("a property changed the index")
	}
	pkgs, _, err := w.st.Packages().List(w.ctx, w.c.Key, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	w.now = w.now.Add(time.Minute)
	w.reindex()
	if diff := cmp.Diff(kept, w.index(w.c)); diff != "" {
		t.Fatal(diff)
	}
	props := w.props("Foo.1.0.0.nupkg")
	if props[nuget.PropID][0] != "Foo" || props[nuget.PropTitle][0] != "T" ||
		props[nuget.PropSummary] != nil || props["keep"][0] != "me" {
		t.Fatalf("%v", props)
	}
	// Nothing to change writes nothing.
	at := w.item("Foo.1.0.0.nupkg").PropertiesModified
	w.now = w.now.Add(time.Minute)
	w.reindex()
	if !w.item("Foo.1.0.0.nupkg").PropertiesModified.Equal(at) {
		t.Fatal("a reindex with nothing to change rewrote the properties")
	}
}

// A remote repository's cache is indexed as downloads store it, whatever
// its names, and is not refused: Enforce Layout's rules are for writes to
// the repository.
func TestUnitRemoteCache(t *testing.T) {
	w := newWorld(t, nil)
	r := w.repo("nuget-remote", repo.Remote, nil)
	for p, body := range map[string][]byte{
		"Foo.1.0.0.nupkg": pkg("Foo", "1.0", ""), "odd/name.nupkg": pkg("Foo", "2.0", ""),
		"garbage.nupkg": []byte("x"),
	} {
		if _, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: r, Cached: true, Path: p}, Body: bytes.NewReader(body),
		}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if got := w.index(r); len(got["foo"]) != 1 {
		t.Fatalf("%v", got)
	}
}

// Inspect reads nothing for a file of another name, or one with no content
// to read.
func TestUnitInspectSkips(t *testing.T) {
	h := nuget.Handler{}
	for _, wr := range []format.Write{
		{Path: "a.txt", Checksums: types.Checksums{SHA256: "x"}},
		{Path: "a.nupkg"},
		{Path: "a.symbols.nupkg", Checksums: types.Checksums{SHA256: "x"}},
	} {
		in, err := h.Inspect(t.Context(), format.Env{}, repo.Config{}, wr)
		if in != nil || err != nil {
			t.Fatalf("%s: %v %v", wr.Path, in, err)
		}
	}
	if _, ok := any(h).(format.Annotator); ok {
		t.Fatal("the format follows properties, but nothing it indexes depends on them")
	}
}

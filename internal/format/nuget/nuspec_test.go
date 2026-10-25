package nuget_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// read reads a package from its bytes.
func read(b []byte) (*nuget.Nuspec, error) {
	return nuget.ReadPackage(bytes.NewReader(b), int64(len(b)))
}

// A full nuspec, as dotnet pack writes one, reads into every field the
// index keeps, each trimmed; the groups win over dependencies outside them,
// as in NuGet's reader.
func TestUnitReadNuspec(t *testing.T) {
	raw := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata minClientVersion=" 4.3 ">
    <id> Contoso.Utility </id>
    <version>01.2.0.0-Preview.2+sha.5</version>
    <title>Contoso Utility</title>
    <authors>Ana, Ben</authors>
    <owners>contoso</owners>
    <requireLicenseAcceptance>TRUE</requireLicenseAcceptance>
    <license type="Expression">MIT OR Apache-2.0</license>
    <licenseUrl>https://aka.ms/deprecateLicenseUrl</licenseUrl>
    <projectUrl>https://contoso.example/utility</projectUrl>
    <iconUrl>https://contoso.example/icon.png</iconUrl>
    <readme>docs/README.md</readme>
    <description>
      Helpers.
    </description>
    <summary>Small helpers</summary>
    <releaseNotes>Fixed things.</releaseNotes>
    <copyright>(c) Contoso</copyright>
    <language>en-GB</language>
    <tags>util helpers</tags>
    <packageTypes>
      <packageType name="Dependency" version="1.0" />
      <packageType name="  " />
    </packageTypes>
    <dependencies>
      <dependency id="Ignored" version="1.0" />
      <group targetFramework=".NETStandard2.0">
        <dependency id="Newtonsoft.Json" version="[13.0.1, )" exclude="Build,Analyzers" />
        <dependency id="" version="1.0" />
      </group>
      <group targetFramework="net8.0" />
    </dependencies>
    <repository type="git" url="https://contoso.example/utility.git" />
  </metadata>
  <files />
</package>`
	n, err := read(pkgWith(zipFile{"Contoso.Utility.nuspec", raw}))
	if err != nil {
		t.Fatal(err)
	}
	accept := true
	want := &nuget.Nuspec{
		ID: "Contoso.Utility", Title: "Contoso Utility", Authors: "Ana, Ben", Owners: "contoso",
		Description: "Helpers.", Summary: "Small helpers", ReleaseNotes: "Fixed things.",
		Copyright: "(c) Contoso", Language: "en-GB", Tags: "util helpers",
		ProjectURL: "https://contoso.example/utility", IconURL: "https://contoso.example/icon.png",
		LicenseURL: "https://aka.ms/deprecateLicenseUrl", LicenseExpression: "MIT OR Apache-2.0",
		RequireLicenseAcceptance: &accept, MinClientVersion: "4.3", Readme: "docs/README.md",
		DependencyGroups: []nuget.DependencyGroup{
			{TargetFramework: ".NETStandard2.0", Dependencies: []nuget.Dependency{
				{ID: "Newtonsoft.Json", Range: "[13.0.1, )"},
			}},
			{TargetFramework: "net8.0"},
		},
		PackageTypes: []nuget.PackageType{{Name: "Dependency", Version: "1.0"}},
		Raw:          []byte(raw),
	}
	want.Version, _ = nuget.ParseVersion("01.2.0.0-Preview.2+sha.5")
	if diff := cmp.Diff(want, n); diff != "" {
		t.Fatal(diff)
	}
	if n.Version.Normalized() != "1.2.0-Preview.2" || !n.IsSemVer2() || n.IsSymbols() {
		t.Fatalf("%s %v %v", n.Version.Normalized(), n.IsSemVer2(), n.IsSymbols())
	}
}

// pkgWith is a zip of files beside a library.
func pkgWith(files ...zipFile) []byte {
	return zipOf(append([]zipFile{{"lib/net8.0/a.dll", "MZ"}}, files...)...)
}

// Every nuspec schema's namespace reads alike, and so does none; the
// nuspec's extension is matched in any case, a byte-order mark is skipped,
// and an encoding declared in another case or as ASCII still reads.
func TestUnitNuspecNamespacesAndCasing(t *testing.T) {
	body := `<metadata><id>MyLIB</id><version>1.0.0-BETA</version></metadata>`
	for _, c := range []struct{ name, nuspec string }{
		{"MyLIB.nuspec", `<package>` + body + `</package>`},
		{"MyLIB.NuSpec", `<package xmlns="http://schemas.microsoft.com/packaging/2010/07/nuspec.xsd">` +
			body + `</package>`},
		{"x.NUSPEC", `<n:package xmlns:n="http://schemas.microsoft.com/packaging/2011/08/nuspec.xsd">` +
			`<n:metadata><n:id>MyLIB</n:id><n:version>1.0.0-BETA</n:version></n:metadata></n:package>`},
		{"MyLIB.nuspec", "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8"?><package>` + body +
			`</package>`},
		{"MyLIB.nuspec", `<?xml version="1.0" encoding="us-ascii"?><package>` + body + `</package>`},
	} {
		n, err := read(pkgWith(zipFile{c.name, c.nuspec}))
		if err != nil {
			t.Fatalf("%s: %v", c.nuspec, err)
		}
		if n.ID != "MyLIB" || n.Version.Normalized() != "1.0.0-BETA" {
			t.Fatalf("%s: %+v", c.nuspec, n)
		}
	}
}

// A nuspec with an id and a version alone leaves every other field empty:
// no licence acceptance at all, rather than false, and no groups. The
// dependencies outside a group, as older nuspecs list them, make one group
// with no framework.
func TestUnitNuspecMissingFields(t *testing.T) {
	n, err := read(pkg("Bare", "1.0", ""))
	if err != nil {
		t.Fatal(err)
	}
	if n.RequireLicenseAcceptance != nil || n.DependencyGroups != nil || n.PackageTypes != nil ||
		n.Title != "" || n.Authors != "" || n.LicenseExpression != "" || n.IsSemVer2() {
		t.Fatalf("%+v", n)
	}
	n, err = read(pkg("Flat", "1.0", `<requireLicenseAcceptance>no</requireLicenseAcceptance>`+
		`<license type="file">LICENSE.txt</license><dependencies>`+
		`<dependency id="A" version="[1.0.0-alpha.1, 2.0)"/><dependency id="B"/></dependencies>`))
	if err != nil {
		t.Fatal(err)
	}
	want := []nuget.DependencyGroup{{Dependencies: []nuget.Dependency{
		{ID: "A", Range: "[1.0.0-alpha.1, 2.0)"}, {ID: "B"},
	}}}
	if diff := cmp.Diff(want, n.DependencyGroups); diff != "" {
		t.Fatal(diff)
	}
	if n.RequireLicenseAcceptance == nil || *n.RequireLicenseAcceptance ||
		n.LicenseExpression != "" {
		t.Fatalf("%+v", n)
	}
	// A range with a SemVer 2 bound makes the package SemVer 2, though its
	// own version is not.
	if n.Version.IsSemVer2() || !n.IsSemVer2() {
		t.Fatal("the range's SemVer 2 bound was missed")
	}
	for _, r := range []string{"1.0.0+meta", "(,2.0.0-rc.1]", "[1.0.0-a.b]"} {
		n, err := read(
			pkg("R", "1.0", `<dependencies><dependency id="A" version="`+r+`"/></dependencies>`),
		)
		if err != nil || !n.IsSemVer2() {
			t.Errorf("%s: %v", r, err)
		}
	}
	for _, r := range []string{"1.0.0", "[1.0, 2.0)", "(,1.0-rc]", "garbage", ""} {
		n, err := read(
			pkg("R", "1.0", `<dependencies><dependency id="A" version="`+r+`"/></dependencies>`),
		)
		if err != nil || n.IsSemVer2() {
			t.Errorf("%s: %v", r, err)
		}
	}
}

// A symbols package declares the SymbolsPackage type, in any case.
func TestUnitIsSymbols(t *testing.T) {
	n, err := read(symbols("Sym", "1.0.0"))
	if err != nil || !n.IsSymbols() {
		t.Fatalf("%v %v", n, err)
	}
	n, err = read(
		pkg("Sym", "1.0.0", `<packageTypes><packageType name="symbolspackage"/></packageTypes>`),
	)
	if err != nil || !n.IsSymbols() {
		t.Fatalf("%v %v", n, err)
	}
}

// What is no valid package says why, and wraps ErrNotPackage.
func TestUnitNotPackages(t *testing.T) {
	long := strings.Repeat("a", nuget.MaxIDLength+1)
	for _, c := range []struct {
		name string
		body []byte
		why  string
	}{
		{"no zip", []byte("not a zip at all"), "no zip"},
		{"a broken zip", []byte("PK\x03\x04 truncated"), "unusable"},
		{"no nuspec", pkgWith(), "no .nuspec at its root"},
		{
			"a nuspec in a folder", pkgWith(zipFile{"meta/A.nuspec", nuspecXML("A", "1.0", "")}),
			"no .nuspec at its root",
		},
		{"two nuspecs", pkgWith(
			zipFile{"A.nuspec", nuspecXML("A", "1.0", "")},
			zipFile{"B.nuspec", nuspecXML("B", "1.0", "")},
		), "more than one .nuspec"},
		{"a huge nuspec", pkgWith(zipFile{"A.nuspec", nuspecXML("A", "1.0",
			"<description>"+strings.Repeat("x", 1<<20)+"</description>")}), "exceeds"},
		{"no XML", pkgWith(zipFile{"A.nuspec", "{}"}), "no XML <package>"},
		{"another root", pkgWith(zipFile{"A.nuspec", "<project/>"}), "no XML <package>"},
		{"no metadata", pkgWith(zipFile{"A.nuspec", "<package/>"}), "no <metadata>"},
		{
			"UTF-16", pkgWith(zipFile{"A.nuspec", `<?xml version="1.0" encoding="utf-16"?><package/>`}),
			"encoded in utf-16",
		},
		{"no id", pkgWith(zipFile{"A.nuspec", nuspecXML("", "1.0", "")}), "not a valid NuGet id"},
		{"a bad id", pkg("a b", "1.0", ""), "not a valid NuGet id"},
		{"a dotted end", pkg("A.", "1.0", ""), "not a valid NuGet id"},
		{"a long id", pkg(long, "1.0", ""), "not a valid NuGet id"},
		{"no version", pkg("A", "", ""), "not a valid NuGet version"},
		{"a bad version", pkg("A", "1.0.0.0.0", ""), "not a valid NuGet version"},
	} {
		_, err := read(c.body)
		if !errors.Is(err, nuget.ErrNotPackage) || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// NuGet's ids allow letters and digits of any script, joined by single
// dots and hyphens, up to 100 UTF-16 code units.
func TestUnitValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"Newtonsoft.Json": true, "a": true, "A-b.C_d": true, "_x": true, "Ünïcödé.Пакет": true,
		"1.2": true, strings.Repeat("a", 100): true, strings.Repeat("a", 101): false,
		strings.Repeat("😀", 50): false, "": false, ".a": false, "a..b": false, "a-": false,
		"a b": false, "a/b": false, "a+b": false,
	} {
		if nuget.ValidID(id) != want {
			t.Errorf("%q: want %v", id, want)
		}
	}
}

// failingBlobs fails to open, or fails reading after the zip's head, so a
// store failure is told from content that is no package.
type failingBlobs struct {
	body   []byte
	broken bool
}

var errBlob = errors.New("blob store down")

func (f failingBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if !f.broken {
		return nil, blob.Info{}, errBlob
	}
	return &brokenReader{r: bytes.NewReader(f.body)}, blob.Info{Size: int64(len(f.body))}, nil
}

func (f failingBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(f.body))}, nil
}

// brokenReader serves the first bytes and fails every read past them.
type brokenReader struct{ r *bytes.Reader }

func (b *brokenReader) Read(p []byte) (int, error) {
	pos, _ := b.r.Seek(0, io.SeekCurrent)
	if pos > 0 {
		return 0, errBlob
	}
	return b.r.Read(p[:min(len(p), 4)])
}

func (b *brokenReader) Seek(off int64, whence int) (int64, error) {
	return b.r.Seek(off, whence) //nolint:wrapcheck // test double
}

func (b *brokenReader) Close() error { return nil }

func TestUnitReadStoredFailures(t *testing.T) {
	body := pkg("A", "1.0", "")
	for _, b := range []failingBlobs{{body: body}, {body: body, broken: true}} {
		_, err := nuget.ReadStored(context.Background(), b, "sha")
		if !errors.Is(err, errBlob) || errors.Is(err, nuget.ErrNotPackage) {
			t.Errorf("broken %v: %v", b.broken, err)
		}
	}
}

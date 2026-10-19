package npm_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
)

func read(t *testing.T, body []byte) (*npm.Tarball, error) {
	t.Helper()
	return npm.ReadTarball(bytes.NewReader(body), int64(len(body)))
}

// package.json is read from the tarball's top-level folder whatever it is
// called; folders, links and entries outside it are no files of the
// package.
func TestReadTarball(t *testing.T) {
	pkg := "\xef\xbb\xbf{ \"name\": \"@acme/left-pad\", \"version\": \"=v1.3.0+build.7\",\n" +
		"  \"description\": \"pads <left>\", \"keywords\": [\"pad\", 3, \"\", \"left\"],\n" +
		"  \"license\": \"(MIT OR Apache-2.0)\" }"
	for _, gz := range []bool{true, false} {
		got, err := read(t, tarOf(gz,
			file{name: "left-pad/"},
			file{name: "left-pad/index.js", body: "module.exports = 1"},
			file{name: "left-pad/lib/package.json", body: `{"name":"nested"}`},
			file{name: "left-pad/link", link: true},
			file{name: "../escape", body: "x"},
			file{name: "./left-pad/package.json", body: pkg},
		))
		if err != nil {
			t.Fatal(err)
		}
		want := &npm.Tarball{
			Name: "@acme/left-pad", Version: "1.3.0",
			Manifest: []byte(`{"name":"@acme/left-pad","version":"=v1.3.0+build.7",` +
				`"description":"pads <left>","keywords":["pad",3,"","left"],` +
				`"license":"(MIT OR Apache-2.0)"}`),
			Description: "pads <left>", Keywords: []string{"pad", "left"},
			License: "(MIT OR Apache-2.0)",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("gzipped %v (-want +got):\n%s", gz, diff)
		}
	}
}

// npm's extraction lets a later entry replace an earlier one of the same
// name, whichever top-level folder each is in.
func TestReadTarballLastPackageJSONWins(t *testing.T) {
	got, err := read(t, tgz(
		file{name: "a/package.json", body: `{"name":"first","version":"1.0.0"}`},
		file{name: "b/package.json", body: `{"name":"second","version":"2.0.0"}`},
	))
	if err != nil || got.Name != "second" || got.Version != "2.0.0" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestReadTarballKeywordsAndScripts(t *testing.T) {
	for _, c := range []struct {
		name     string
		fields   string
		gyp      bool
		keywords []string
		install  bool
	}{
		{
			"keywords as one string", `"keywords":"pad,  left,right"`, false,
			[]string{"pad", "left,right"},
			false,
		},
		{"keywords neither", `"keywords":{"a":1}`, false, nil, false},
		{"install", `"scripts":{"install":"make"}`, false, nil, true},
		{"preinstall", `"scripts":{"preinstall":"make"}`, false, nil, true},
		{"postinstall", `"scripts":{"postinstall":"make","test":"x"}`, false, nil, true},
		{"empty script", `"scripts":{"install":""}`, false, nil, false},
		{"other scripts", `"scripts":{"test":"x"}`, false, nil, false},
		{"binding.gyp", `"description":7`, true, nil, true},
		{"gypfile false", `"gypfile":false`, true, nil, false},
		{"gypfile true", `"gypfile":true`, true, nil, true},
	} {
		files := []file{{
			name: "package/package.json",
			body: `{"name":"x","version":"1.0.0",` + c.fields + `}`,
		}}
		if c.gyp {
			files = append(files, file{name: "package/binding.gyp", body: "{}"})
		}
		got, err := read(t, tgz(files...))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slicesEqual(got.Keywords, c.keywords) || got.HasInstallScript != c.install ||
			got.Description != "" {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

// Only a license given as a string, an SPDX expression, is read: what 7.x
// makes of the legacy object and array forms is unknown.
func TestReadTarballLicense(t *testing.T) {
	for fields, want := range map[string]string{
		`"license":"MIT"`:                    "MIT",
		`"license":{"type":"MIT","url":"x"}`: "",
		`"licenses":[{"type":"MIT"}]`:        "",
		`"license":7`:                        "",
		`"description":"no license"`:         "",
	} {
		got, err := read(t, tgz(file{
			name: "package/package.json",
			body: `{"name":"x","version":"1.0.0",` + fields + `}`,
		}))
		if err != nil || got.License != want {
			t.Errorf("%s: %+v %v, want %q", fields, got, err, want)
		}
	}
}

func slicesEqual(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

// Content the format cannot index is ErrNotTarball, so it is stored but
// not indexed.
func TestReadTarballRefusals(t *testing.T) {
	big := strings.Repeat(" ", 1<<20)
	for name, body := range map[string][]byte{
		"zip":             zipOf(t),
		"garbage":         []byte("not a tarball at all"),
		"corrupt gzip":    append([]byte{0x1f, 0x8b, 8, 0}, bytes.Repeat([]byte{0xff}, 600)...),
		"no package.json": tgz(file{name: "package/index.js", body: "x"}),
		"at the root":     tgz(file{name: "package.json", body: `{"name":"x","version":"1.0.0"}`}),
		"too deep":        tgz(file{name: "a/b/package.json", body: `{"name":"x","version":"1.0.0"}`}),
		"not JSON":        tgz(file{name: "package/package.json", body: `{"name":`}),
		"not an object":   tgz(file{name: "package/package.json", body: `["x"]`}),
		"null":            tgz(file{name: "package/package.json", body: `null`}),
		"no name":         tgz(file{name: "package/package.json", body: `{"version":"1.0.0"}`}),
		"invalid name":    tgz(file{name: "package/package.json", body: `{"name":".x","version":"1.0.0"}`}),
		"name not string": tgz(file{name: "package/package.json", body: `{"name":1,"version":"1.0.0"}`}),
		"no version":      tgz(file{name: "package/package.json", body: `{"name":"x"}`}),
		"version not semver": tgz(file{
			name: "package/package.json",
			body: `{"name":"x","version":"1.0"}`,
		}),
		"package.json too big": tgz(file{
			name: "package/package.json",
			body: `{"name":"x","version":"1.0.0","d":"` + big + `"}`,
		}),
	} {
		if _, err := read(t, body); !errors.Is(err, npm.ErrNotTarball) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func zipOf(t *testing.T) []byte {
	t.Helper()
	// A zip's local file header: archive.Sniff tells it from a tar by it.
	return append([]byte("PK\x03\x04"), make([]byte, 600)...)
}

// A tarball past the entry or inflation limits is not indexed rather than
// read without end.
func TestReadTarballLimits(t *testing.T) {
	body := tgz(
		file{name: "package/a", body: "1"}, file{name: "package/b", body: "2"},
		file{name: "package/package.json", body: `{"name":"x","version":"1.0.0"}`},
	)
	defer npm.SetLimits(archive.Limits{Entries: 2, Inflated: 1 << 20, File: 1 << 20})()
	if _, err := read(t, body); !errors.Is(err, npm.ErrNotTarball) {
		t.Fatalf("entries: %v", err)
	}
	npm.SetLimits(archive.Limits{Entries: 10, Inflated: 1024, File: 1 << 20})
	padded := tgz(file{name: "package/pad", body: strings.Repeat("0", 1<<16)},
		file{name: "package/package.json", body: `{"name":"x","version":"1.0.0"}`})
	if _, err := read(t, padded); !errors.Is(err, npm.ErrNotTarball) {
		t.Fatalf("inflated: %v", err)
	}
}

var errDisk = errors.New("disk on fire")

// failing fails every read past its first n bytes.
type failing struct {
	r *bytes.Reader
	n int64
}

func (f failing) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.n {
		return 0, errDisk
	}
	return f.r.ReadAt(p, off) //nolint:wrapcheck // test double
}

// A store that fails mid-read is a failure, not content to leave
// unindexed.
func TestReadTarballReadFailure(t *testing.T) {
	body := tgz(file{name: "package/package.json", body: `{"name":"x","version":"1.0.0"}`},
		file{name: "package/pad", body: strings.Repeat("x", 1<<16)})
	_, err := npm.ReadTarball(failing{bytes.NewReader(body), 64}, int64(len(body)))
	if !errors.Is(err, errDisk) || errors.Is(err, npm.ErrNotTarball) {
		t.Fatalf("%v", err)
	}
	// The end of the content is no failure.
	short := failing{bytes.NewReader(body), 1 << 30}
	if _, err := npm.ReadTarball(short, int64(len(body))); err != nil {
		t.Fatal(err)
	}
}

func TestCleanVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3":                   "1.2.3",
		" v1.2.3 ":                "1.2.3",
		"=v=1.2.3":                "1.2.3",
		"0.0.0":                   "0.0.0",
		"1.2.3-beta.1":            "1.2.3-beta.1",
		"1.2.3-0a.x-y.0":          "1.2.3-0a.x-y.0",
		"1.2.3+build.5":           "1.2.3",
		"1.2.3-rc.1+b":            "1.2.3-rc.1",
		"9007199254740991.0.0":    "9007199254740991.0.0",
		"1.2.3-99999999999999999": "1.2.3-99999999999999999",
	} {
		if got, ok := npm.CleanVersion(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.3-01", "1.2.3-",
		"1.2.3+", "1.2.3-a..b", "1.2.3_x", "a.b.c", "9007199254740992.0.0",
		"10000000000000000.0.0", "1.2.3-" + strings.Repeat("a", npm.MaxVersionLength),
	} {
		if got, ok := npm.CleanVersion(in); ok {
			t.Errorf("%q: %q accepted", in, got)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{
		"left-pad", "@acme/left-pad", "JSONStream", "a.b", "~x", "x_y", "(x)", "@a/.b",
	} {
		if !npm.ValidName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{
		"", ".x", "_x", "node_modules", "favicon.ico", "a/b", "@a", "@/b", "@a/", "a b",
		"a%2fb", "@a/b/c", "é", "@a@/b",
	} {
		if npm.ValidName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestCanonicalPath(t *testing.T) {
	for _, c := range [][3]string{
		{"left-pad", "1.3.0", "left-pad/-/left-pad-1.3.0.tgz"},
		{"@acme/pad", "2.0.0-rc.1", "@acme/pad/-/@acme/pad-2.0.0-rc.1.tgz"},
	} {
		if got := npm.CanonicalPath(c[0], c[1]); got != c[2] {
			t.Errorf("%v: %s", c, got)
		}
	}
}

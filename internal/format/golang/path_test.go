package golang_test

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/mod/module"

	"github.com/weaveplatform/artifactweave/internal/format/golang"
)

// The go command sends module paths and versions case-encoded and jf sends
// them as written; both name the same file, stored as written.
func TestParseRequest(t *testing.T) {
	for p, want := range map[string]golang.Request{
		"example.com/m/@v/list":        {Kind: golang.KindList, Module: "example.com/m"},
		"example.com/m/@latest":        {Kind: golang.KindLatest, Module: "example.com/m"},
		"example.com/m/@v/latest.info": {Kind: golang.KindLatest, Module: "example.com/m"},
		"example.com/m/@v/v1.0.0.info": {
			Kind: golang.KindInfo, Module: "example.com/m", Version: "v1.0.0",
		},
		"example.com/m/@v/v1.0.0.mod": {
			Kind: golang.KindMod, Module: "example.com/m", Version: "v1.0.0",
		},
		"example.com/m/@v/v1.0.0-rc.1.zip": {
			Kind: golang.KindZip, Module: "example.com/m", Version: "v1.0.0-rc.1",
		},
		"github.com/!azure/!s!d!k/@v/v1.0.0-!r!c1.zip": {
			Kind: golang.KindZip, Module: "github.com/Azure/SDK", Version: "v1.0.0-RC1",
		},
		"github.com/Azure/SDK/@v/v1.0.0-RC1.zip": {
			Kind: golang.KindZip, Module: "github.com/Azure/SDK", Version: "v1.0.0-RC1",
		},
		"github.com/!azure/!s!d!k/@v/list": {Kind: golang.KindList, Module: "github.com/Azure/SDK"},
		"example.com/m/@v/v4.1.2+incompatible.zip": {
			Kind: golang.KindZip, Module: "example.com/m", Version: "v4.1.2+incompatible",
		},
		"example.com/m/v2/@v/v2.0.1.mod": {
			Kind: golang.KindMod, Module: "example.com/m/v2", Version: "v2.0.1",
		},
		"example.com/m/@v/v0.0.0-20191109021931-daa7c04131f5.info": {
			Kind: golang.KindInfo, Module: "example.com/m",
			Version: "v0.0.0-20191109021931-daa7c04131f5",
		},
	} {
		got, err := golang.ParseRequest(p)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v; want %+v", p, got, err, want)
		}
	}
}

// The go command's spelling of any module path and version reads back as
// the module and version it encoded.
func TestParseRequestDecodesWhatTheGoCommandEncodes(t *testing.T) {
	for _, mv := range []module.Version{
		{Path: "github.com/BurntSushi/toml", Version: "v1.4.0"},
		{Path: "example.com/Upper/Case/v3", Version: "v3.0.0-Beta.1"},
	} {
		m, err := module.EscapePath(mv.Path)
		if err != nil {
			t.Fatal(err)
		}
		v, err := module.EscapeVersion(mv.Version)
		if err != nil {
			t.Fatal(err)
		}
		got, err := golang.ParseRequest(m + "/@v/" + v + ".zip")
		if err != nil || got.Module != mv.Path || got.Version != mv.Version {
			t.Errorf("%s@%s: %+v, %v", m, v, got, err)
		}
		if p := got.File().Path(); p != mv.Path+"/@v/"+mv.Version+".zip" {
			t.Errorf("stored at %s", p)
		}
	}
}

// A path naming no operation answers 404 to every method; one naming an
// operation but failing its checks answers 404 to a read and 400, with the
// reason, to a PUT.
func TestParseRequestRefuses(t *testing.T) {
	for p, invalid := range map[string]bool{
		"":                                         false,
		"example.com/m":                            false,
		"example.com/m/v1.0.0.zip":                 false,
		"@v/list":                                  false,
		"/@latest":                                 false,
		"example.com/m/@v/v1.0.0/x.zip":            false,
		"example.com/m/@v/v1.0.0.jar":              true,
		"example.com/m/@v/.zip":                    true,
		"example.com/m/@v/master.info":             true,
		"example.com/m/@v/v1.0.info":               true,
		"example.com/m/@v/v1.0.0+build.zip":        true,
		"example.com/m/@v/v2.0.0.zip":              true,
		"example.com/m/@v/v1.0.0+incompatible.zip": true,
		"example.com/m/v2/@v/v3.0.0.zip":           true,
		"example/m/@v/v1.0.0.zip":                  true,
		"example.com/!!m/@v/v1.0.0.zip":            true,
		"example.com/m/@v/v1.0.0-!!x.zip":          true,
		"example/m/@v/list":                        true,
		"example.com/!!m/@latest":                  true,
	} {
		_, err := golang.ParseRequest(p)
		var bad *golang.Invalid
		switch {
		case invalid && !errors.As(err, &bad):
			t.Errorf("%q: %v; want Invalid", p, err)
		case invalid && (err.Error() != bad.Message || !strings.HasSuffix(bad.Message, ".")):
			t.Errorf("%q: %q is no sentence", p, bad.Message)
		case invalid:
		case !errors.Is(err, golang.ErrNoOperation):
			t.Errorf("%q: %v; want ErrNoOperation", p, err)
		}
	}
}

// A stored file is indexed only at the path jf go-publish deploys it to:
// never case-encoded, with a canonical version and a zip, mod or info
// extension.
func TestParseFile(t *testing.T) {
	for p, want := range map[string]golang.File{
		"github.com/Azure/SDK/@v/v1.2.3.zip": {
			Module: "github.com/Azure/SDK", Version: "v1.2.3", Ext: golang.ExtZip,
		},
		"example.com/m/v2/@v/v2.0.0.mod": {
			Module: "example.com/m/v2", Version: "v2.0.0", Ext: golang.ExtMod,
		},
		"example.com/m/@v/v2.0.0+incompatible.info": {
			Module: "example.com/m", Version: "v2.0.0+incompatible", Ext: golang.ExtInfo,
		},
	} {
		got, ok := golang.ParseFile(p)
		if !ok || got != want || got.Path() != p {
			t.Errorf("%s: %+v, %v; want %+v", p, got, ok, want)
		}
	}
	for _, p := range []string{
		"github.com/!azure/!s!d!k/@v/v1.2.3.zip",
		"example.com/m/@v/list",
		"example.com/m/@v/v1.0.zip",
		"example.com/m/@v/v1.0.0.txt",
		"example.com/m/v1.0.0.zip",
		"@v/v1.0.0.zip",
		"example/m/@v/v1.0.0.zip",
	} {
		if got, ok := golang.ParseFile(p); ok {
			t.Errorf("%s: %+v", p, got)
		}
	}
}

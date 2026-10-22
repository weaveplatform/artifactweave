package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
)

func (w *world) registerLegacyRemoteNuGet(sc *godog.ScenarioContext) {
	sc.Step(`^a private NuGet v2 upstream serves packages from "([^"]*)"$`, w.legacyNuGetUpstream)
	sc.Step(`^a private symbol upstream serves PDBs from "([^"]*)"$`, w.symbolNuGetUpstream)
}

func (w *world) newPrivateNuGetUpstream() {
	w.remote = fakeupstream.New(
		w.t,
		fakeupstream.Options{
			Auth: fakeupstream.Auth{Username: "upstream", Password: "private-fixture"},
		},
	)
	w.closers = append(w.closers, w.remote.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
}

func nativeXMLEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func nativeAtomFeed(entries, next string) string {
	if next != "" {
		next = `<link rel="next" href="` + nativeXMLEscape(next) + `"/>`
	}
	return `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">` + entries + next + `</feed>`
}

func (w *world) legacyNuGetUpstream(folder string) error {
	w.newPrivateNuGetUpstream()
	files, err := filepath.Glob(filepath.Join(w.dir, folder, "*.nupkg"))
	if err != nil {
		return err
	}
	if len(files) < 2 {
		return fmt.Errorf("v2 acceptance needs at least two package versions")
	}
	entries := make([]string, 0, len(files))
	for _, file := range files {
		raw, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		n, e := nuget.ReadPackage(bytes.NewReader(raw), int64(len(raw)))
		if e != nil {
			return e
		}
		ver := n.Version.Normalized()
		digest := sha512.Sum512(raw)
		path := "/custom/download/" + n.ID + "/" + ver
		binary := "/archives/" + filepath.Base(file)
		w.remote.Set(path, fakeupstream.Resource{Redirect: binary})
		w.remote.Set(
			binary,
			fakeupstream.Resource{
				Body:   raw,
				Header: http.Header{"Content-Type": {"application/octet-stream"}},
			},
		)
		entry := `<entry xmlns="http://www.w3.org/2005/Atom" xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" xmlns:d="http://schemas.microsoft.com/ado/2007/08/dataservices"><id>` + nativeXMLEscape(
			w.remote.URL+path,
		) + `</id><title>` + n.ID + `</title><author><name>artifactweave</name></author><content type="application/zip" src="` + nativeXMLEscape(
			w.remote.URL+path,
		) + `"/><m:properties><d:Id>` + n.ID + `</d:Id><d:Version>` + ver + `</d:Version><d:Description>Remote legacy native fixture</d:Description><d:Published>2026-10-22T00:00:00Z</d:Published><d:Dependencies></d:Dependencies><d:PackageHash>` + base64.StdEncoding.EncodeToString(
			digest[:],
		) + `</d:PackageHash><d:PackageHashAlgorithm>SHA512</d:PackageHashAlgorithm></m:properties></entry>`
		entries = append(entries, entry)
	}
	set := func(path, body string) {
		w.remote.Set(
			path,
			fakeupstream.Resource{
				Body:   []byte(body),
				Header: http.Header{"Content-Type": {"application/atom+xml"}},
			},
		)
	}
	set(
		"/custom/feed",
		`<service xmlns="http://www.w3.org/2007/app"><workspace><collection href="Packages"/></workspace></service>`,
	)
	set(
		"/custom/feed/$metadata",
		`<edmx:Edmx xmlns:edmx="http://schemas.microsoft.com/ado/2007/06/edmx" Version="1.0"/>`,
	)
	w.remote.Set(
		"/custom/feed/FindPackagesById()",
		fakeupstream.Resource{Redirect: "/relocated/feed.xml"},
	)
	set("/relocated/feed.xml", nativeAtomFeed(entries[0], "next.xml"))
	set("/relocated/next.xml", nativeAtomFeed(strings.Join(entries[1:], ""), ""))
	for _, path := range []string{"/custom/feed/Packages", "/custom/feed/Packages()", "/custom/feed/Search()"} {
		set(path, nativeAtomFeed(strings.Join(entries, ""), ""))
	}
	return nil
}

func (w *world) symbolNuGetUpstream(file string) error {
	raw, err := os.ReadFile(filepath.Join(w.dir, file))
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	w.newPrivateNuGetUpstream()
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".pdb") {
			continue
		}
		rc, e := f.Open()
		if e != nil {
			return e
		}
		body, e := io.ReadAll(rc)
		_ = rc.Close()
		if e != nil {
			return e
		}
		key, ok := nuget.PDBKey(body)
		if !ok {
			return fmt.Errorf("native PDB lacks supported key")
		}
		name := strings.ToLower(filepath.Base(f.Name))
		path := name + "/" + key + "/" + name
		w.remote.Set(
			"/symbols/"+path,
			fakeupstream.Resource{
				Body:   body,
				Header: http.Header{"Content-Type": {"application/octet-stream"}},
			},
		)
		w.vars["symbol-route"] = "/api/nuget/v3/nuget-remote/" + path
		w.vars["symbol-key"] = key
		w.vars["symbol-size"] = strconv.Itoa(len(body))
		if e = os.WriteFile(filepath.Join(w.dir, "native.pdb"), body, 0o600); e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(w.dir, "native-prefix.pdb"), body[:16], 0o600); e != nil {
			return e
		}
		return nil
	}
	return fmt.Errorf("native symbols package contains no PDB")
}

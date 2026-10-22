package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestRemoteV2XMLAdmissionNeverPublishes(t *testing.T) {
	valid := legacyEntryXML(
		"Acme.Mixed",
		"1.0.0",
		"https://upstream.example/package",
		nupkg("Acme.Mixed", "1.0.0", ""),
	)
	cases := map[string]string{
		"malformed":    `<feed>`,
		"empty":        "",
		"outside-text": "secret" + legacyFeedXML(valid, ""),
		"wrong-root":   `<rss/>`,
		"no-properties": legacyFeedXML(
			`<entry xmlns="`+remoteAtomNS+`"><content src="https://upstream.example/p"/></entry>`,
			"",
		),
		"nested-property": legacyFeedXML(
			strings.Replace(valid, `<d:Id>Acme.Mixed</d:Id>`, `<d:Id><x>Acme.Mixed</x></d:Id>`, 1),
			"",
		),
		"wrong-property-ns": legacyFeedXML(
			strings.Replace(
				valid,
				`<d:Description>Remote package</d:Description>`,
				`<Description>Remote package</Description>`,
				1,
			),
			"",
		),
		"duplicate-content": legacyFeedXML(
			strings.Replace(
				valid,
				`<m:properties>`,
				`<content src="https://upstream.example/p"/><m:properties>`,
				1,
			),
			"",
		),
		"unsafe-base": legacyFeedXML(
			strings.Replace(
				valid,
				`<entry `,
				`<entry xml:base="https://user:secret@upstream.example/" `,
				1,
			),
			"",
		),
		"bad-dependency": legacyFeedXML(
			strings.Replace(
				valid,
				`</m:properties>`,
				`<d:Dependencies>bad/id:1.0:net8.0</d:Dependencies></m:properties>`,
				1,
			),
			"",
		),
		"bad-base64": legacyFeedXML(
			strings.Replace(valid, `<d:PackageHash>`, `<d:PackageHash>???`, 1),
			"",
		),
		"duplicate-next": legacyFeedXML(valid, "/next")[:len(legacyFeedXML(valid, "/next"))-7] + `<link rel="next" href="/other"/></feed>`,

		"doctype": `<!DOCTYPE feed [<!ENTITY secret SYSTEM "file:///etc/passwd">]><feed xmlns="` + remoteAtomNS + `">&secret;</feed>`,
		"entity":  `<!DOCTYPE feed [<!ENTITY a "AAAAAAAAAA">]><feed xmlns="` + remoteAtomNS + `">&a;</feed>`,
		"wrong-namespace": strings.Replace(
			legacyFeedXML(valid, ""),
			remoteAtomNS,
			"https://forged.example/atom",
			1,
		),
		"trailing":     legacyFeedXML(valid, "") + `<extra/>`,
		"instructions": `<?fetch file="file:///secret"?>` + legacyFeedXML(valid, ""),
		"duplicate-attribute": strings.Replace(
			legacyFeedXML(valid, ""),
			`type="application/zip"`,
			`type="application/zip" type="text/html"`,
			1,
		),
		"duplicate-property": strings.Replace(
			legacyFeedXML(valid, ""),
			`</d:Id>`,
			`</d:Id><d:Id>Other</d:Id>`,
			1,
		),
		"duplicate-version": legacyFeedXML(valid+valid, ""),
		"wrong-id": legacyFeedXML(
			strings.ReplaceAll(valid, "Acme.Mixed", "Other.Package"),
			"",
		),
		"bad-version": legacyFeedXML(
			strings.ReplaceAll(valid, "1.0.0", "not-a-version"),
			"",
		),
		"bad-hash": legacyFeedXML(strings.Replace(valid, "SHA512", "MD5", 1), ""),
		"credential": legacyFeedXML(
			strings.Replace(
				valid,
				"https://upstream.example/package",
				"https://secret:password@upstream.example/package",
				1,
			),
			"",
		),
		"ssrf": legacyFeedXML(valid, "http://169.254.169.254/latest"),
		"depth": `<feed xmlns="` + remoteAtomNS + `">` + strings.Repeat(
			"<x>",
			33,
		) + strings.Repeat(
			"</x>",
			33,
		) + `</feed>`,
		"oversize": `<feed xmlns="` + remoteAtomNS + `">` + strings.Repeat(
			"x",
			16<<20,
		) + `</feed>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLegacyFixture(t)
			f.setXML("/custom/feed/FindPackagesById()", body)
			wantStatus(
				t,
				f.do("GET", remoteV2+"FindPackagesById()?id=%27Acme.Mixed%27", "partial"),
				502,
			)
			entries, _, err := f.db.RemoteCache().
				List(t.Context(), "nuget-remote", "", store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Blob.Checksums.SHA256 != "" {
					t.Fatal("invalid XML was admitted")
				}
			}
			f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(valid, ""))
			wantStatus(
				t,
				f.do("GET", remoteV2+"FindPackagesById()?id=%27Acme.Mixed%27", "partial"),
				200,
			)
		})
	}
}

func TestRemoteV2RedirectBaseAndCycles(t *testing.T) {
	f := newLegacyFixture(t)
	first := f.legacyPackage("Acme.Mixed", "1.0.0")
	second := f.legacyPackage("Acme.Mixed", "2.0.0")
	f.up.Set(
		"/custom/feed/FindPackagesById()",
		fakeupstream.Resource{Redirect: "/relocated/root/feed.xml"},
	)
	f.setXML("/relocated/root/feed.xml", legacyFeedXML(first, "../pages/second.xml"))
	f.setXML("/relocated/pages/second.xml", legacyFeedXML(second, ""))
	rec := f.do("GET", remoteV2+"FindPackagesById()?id=%27Acme.Mixed%27", "reader")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "2.0.0") ||
		f.up.Count("GET", "/relocated/pages/second.xml") != 1 {
		t.Fatal("redirected base was lost", f.up.Requests())
	}
	// Relative xml:base changes scope once, including on a standalone Atom entry.
	f.setXML(
		"/custom/feed/Packages",
		strings.Replace(first, `<entry `, `<entry xml:base="`+f.up.URL+`/nested/" `, 1),
	)
	wantStatus(t, f.do("GET", remoteV2+"Packages", "reader"), 200)
	f.setXML("/custom/feed/Search()", legacyFeedXML(first, "/loop"))
	f.setXML("/loop", legacyFeedXML(second, "/loop"))
	wantStatus(t, f.do("GET", remoteV2+"Search()", "reader"), 502)
}

func TestRemoteV2QueryDenialsAndMissing(t *testing.T) {
	f := newLegacyFixture(t)
	f.setXML(
		"/custom/feed/FindPackagesById()",
		legacyFeedXML(f.legacyPackage("Acme.Mixed", "1.0.0"), ""),
	)
	for _, c := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", "FindPackagesById()?id=broken", "reader", 400},
		{"GET", "FindPackagesById()?id=%27bad/id%27", "reader", 400},
		{"GET", "Packages?$orderby=Version", "reader", 400},
		{"GET", "Packages?$select=Id", "reader", 400},
		{"GET", "Packages?$filter=contains(Id,%27Acme%27)", "reader", 400},
		{"GET", "Packages?$skip=-1", "reader", 400},
		{"GET", "GetUpdates()", "reader", 404},
		{"GET", "Unknown/x", "reader", 404},
		{"GET", "Download/Acme.Mixed/bad-version", "reader", 404},
		{"GET", "Download/Acme.Mixed/2.0.0", "reader", 404},
		{"GET", "Packages(Id='Acme.Mixed',Version='2.0.0')", "reader", 404},
		{"GET", "FindPackagesById()?id=%27Acme.Mixed%27", "nobody", 403},
		{"PUT", "", "reader", 405},
		{"DELETE", "Acme.Mixed/1.0.0", "reader", 405},
	} {
		wantStatus(t, f.do(c.method, remoteV2+c.path, c.user), c.status)
	}
}

func TestRemoteV2FinalDisclosedPathFence(t *testing.T) {
	f := newLegacyFixture(t)
	f.setXML(
		"/custom/feed/Packages",
		legacyFeedXML(
			f.legacyPackage("Acme.Mixed", "1.0.0")+f.legacyPackage("Acme.Mixed", "2.0.0"),
			"",
		),
	)
	original := f.svc.HTTP
	revoked := false
	f.svc.HTTP = backendFunc(
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			r, err := original.Read(ctx, q, p)
			if err == nil && r.Stream != nil {
				r.Stream.Body = &closeAction{
					ReadCloser: r.Stream.Body,
					after:      func() { revoked = true },
				}
			}
			return r, err
		},
	)
	f.svc.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if revoked && q.Resource.Path == "acme.mixed/2.0.0/acme.mixed.2.0.0.nupkg" {
			return source.ErrDenied
		}
		return nil
	}
	rec := f.do("GET", remoteV2+"Packages", "reader")
	wantStatus(t, rec, http.StatusForbidden)
	if strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal("revoked version leaked")
	}
}

func TestRemoteV2NoContentOnlyOnFeedEndpoints(t *testing.T) {
	f := newLegacyFixture(t)
	for _, endpoint := range []string{"Packages", "Search()", "FindPackagesById()"} {
		f.up.Set("/custom/feed/"+endpoint, fakeupstream.Resource{Status: 204})
		suffix := ""
		if endpoint == "FindPackagesById()" {
			suffix = "?id=%27Acme.Mixed%27"
		}
		rec := f.do("GET", remoteV2+endpoint+suffix, "reader")
		wantStatus(t, rec, 200)
		if !strings.Contains(rec.Body.String(), "<feed") ||
			strings.Contains(rec.Body.String(), "<entry") {
			t.Fatal("not empty Atom", rec.Body.String())
		}
		count := f.do("GET", remoteV2+endpoint+"/$count"+suffix, "reader")
		wantStatus(t, count, 200)
		if count.Body.String() != "0" {
			t.Fatal("empty count", count.Body.String())
		}
	}
	for _, endpoint := range []string{"", "/$metadata"} {
		f.up.Set("/custom/feed"+endpoint, fakeupstream.Resource{Status: 204})
		wantStatus(t, f.do("GET", strings.TrimRight(remoteV2, "/")+endpoint, "reader"), 502)
	}
}

func TestRemoteV2CredentialRotationAndExpiry(t *testing.T) {
	f := newLegacyFixture(
		t,
		fakeupstream.Options{Auth: fakeupstream.Auth{Username: "up", Password: "secret"}},
	)
	p := remoteV2 + "FindPackagesById()?id=%27Acme.Mixed%27"
	wantStatus(t, f.do("GET", p, "reader"), 401)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.Username, c.Password = "up", "secret"
	c.ForceNugetAuthentication = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	first := f.legacyPackage("Acme.Mixed", "1.0.0")
	f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(first, ""))
	wantStatus(t, f.do("GET", p, "anonymous"), 401)
	wantStatus(t, f.do("GET", p, "reader"), 200)
	second := f.legacyPackage("Acme.Mixed", "2.0.0")
	f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(first+second, ""))
	if strings.Contains(f.do("GET", p, "reader").Body.String(), "2.0.0") {
		t.Fatal("metadata TTL bypassed")
	}
	f.clock.Advance(6 * time.Second)
	if !strings.Contains(f.do("GET", p, "reader").Body.String(), "2.0.0") {
		t.Fatal("expired feed stayed stale")
	}
	c.Password = "revoked"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("GET", p, "reader"), 401)
}

func TestRemoteV2NoStorePinsAndArchiveAdmission(t *testing.T) {
	for _, fault := range []string{"checksum", "crc", "traversal", "html"} {
		t.Run(fault, func(t *testing.T) {
			f := newLegacyFixture(t)
			c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
			c.StoreArtifactsLocally = new(false)
			c.RemoteRepoChecksumPolicyType = repo.IgnoreAndGenerate
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			body := nupkg("Acme.Mixed", "1.0.0", "")
			entry := legacyEntryXML("Acme.Mixed", "1.0.0", f.up.URL+"/package", body)
			switch fault {
			case "checksum":
				body = []byte("changed")
			case "crc":
				body = bytes.Replace(body, []byte("MZ 1.0.0"), []byte("MZ 9.0.0"), 1)
			case "html":
				body = []byte("<html>login</html>")
			case "traversal":
				zr, _ := zip.NewReader(bytes.NewReader(body), int64(len(body)))
				var b bytes.Buffer
				zw := zip.NewWriter(&b)
				for _, file := range zr.File {
					_ = zw.Copy(file)
				}
				w, _ := zw.Create("%2e%2e/outside")
				_, _ = io.WriteString(w, "bad")
				_ = zw.Close()
				body = b.Bytes()
			}
			if fault != "checksum" {
				entry = legacyEntryXML("Acme.Mixed", "1.0.0", f.up.URL+"/package", body)
			}
			f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(entry, ""))
			f.up.Set("/custom/packages/Acme.Mixed/1.0.0", fakeupstream.Resource{Body: body})
			wantStatus(t, f.do("GET", remoteV2+"Download/Acme.Mixed/1.0.0", "reader"), 502)
			if _, err := f.db.Items().
				Get(t.Context(), c.Key, true, "acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("invalid no-store package persisted", err)
			}
		})
	}
}

func TestRemoteV2ServiceSchemaAdmission(t *testing.T) {
	for _, tc := range []struct{ endpoint, body string }{
		{"", `<html/>`},
		{"/$metadata", `<feed/>`},
		{"", `<service xmlns="http://www.w3.org/2007/app"><workspace><collection href="https://user:secret@upstream.example/"/></workspace></service>`},
		{"", `<service xmlns="http://www.w3.org/2007/app" xml:base="http://169.254.169.254/"><workspace/></service>`},
	} {
		f := newLegacyFixture(t)
		f.setXML("/custom/feed"+tc.endpoint, tc.body)
		wantStatus(t, f.do("GET", strings.TrimRight(remoteV2, "/")+tc.endpoint, "reader"), 502)
	}
}

func TestRemoteV2UpstreamNoStoreFeedIsNeverReused(t *testing.T) {
	f := newLegacyFixture(t)
	first := f.legacyPackage("Acme.Mixed", "1.0.0")
	set := func(entries string) {
		f.up.Set(
			"/custom/feed/Packages",
			fakeupstream.Resource{
				Body: []byte(legacyFeedXML(entries, "")),
				Header: http.Header{
					"Content-Type":  {"application/atom+xml"},
					"Cache-Control": {"no-store"},
				},
			},
		)
	}
	set(first)
	wantStatus(t, f.do("GET", remoteV2+"Packages", "reader"), 200)
	set(first + f.legacyPackage("Acme.Mixed", "2.0.0"))
	rec := f.do("GET", remoteV2+"Packages", "reader")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal("no-store feed reused")
	}
	entries, _, err := f.db.RemoteCache().List(t.Context(), "nuget-remote", "", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Blob.Checksums.SHA256 != "" {
			t.Fatal("no-store XML persisted")
		}
	}
}

func TestRemoteV2ForeignContinuationRefusedBeforeFetch(t *testing.T) {
	f := newLegacyFixture(t)
	foreign := fakeupstream.New(t, fakeupstream.Options{})
	foreign.Set(
		"/page",
		fakeupstream.Resource{
			Body:   []byte(legacyFeedXML("", "")),
			Header: http.Header{"Content-Type": {"application/atom+xml"}},
		},
	)
	f.setXML(
		"/custom/feed/Packages",
		legacyFeedXML(f.legacyPackage("Acme.Mixed", "1.0.0"), foreign.URL+"/page"),
	)
	wantStatus(t, f.do("GET", remoteV2+"Packages", "reader"), 502)
	if len(foreign.Requests()) != 0 {
		t.Fatal("unconfigured private continuation fetched")
	}
}

func TestRemoteV2AndSymbolNoStoreSourceRevocation(t *testing.T) {
	for _, kind := range []string{"feed", "symbol"} {
		for _, change := range []string{"source", "credential"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f := newLegacyFixture(t)
				c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
				c.StoreArtifactsLocally = new(false)
				c.SymbolServerURL = f.up.URL + "/symbols"
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				path := remoteV2 + "Packages"
				f.setXML(
					"/custom/feed/Packages",
					legacyFeedXML(f.legacyPackage("Acme.Mixed", "1.0.0"), ""),
				)
				if kind == "symbol" {
					symbolPath := "app.pdb/" + remotePDBKey + "/app.pdb"
					path = remoteV3 + symbolPath
					f.up.Set("/symbols/"+symbolPath, fakeupstream.Resource{Body: remotePDB()})
				}
				revoke := func() {
					current, err := f.db.Repos().Get(t.Context(), c.Key)
					if err != nil {
						t.Fatal(err)
					}
					if change == "source" {
						current.URL = f.up.URL + "/changed"
					} else {
						current.Password = "rotated-after-complete-read"
					}
					if err = f.db.Repos().Update(t.Context(), current); err != nil {
						t.Fatal(err)
					}
				}
				original := f.svc.HTTP
				f.svc.HTTP = backendFunc(
					func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
						out, err := original.Read(ctx, q, p)
						if err == nil && out.Stream != nil {
							if kind == "feed" {
								out.Stream.Body = &closeAction{
									ReadCloser: out.Stream.Body,
									after:      revoke,
								}
							} else {
								revoke()
							}
						}
						return out, err
					},
				)
				rec := f.do("GET", path, "reader")
				wantStatus(t, rec, 503)
				if strings.Contains(rec.Body.String(), "Acme.Mixed") ||
					bytes.Contains(rec.Body.Bytes(), []byte("BSJB")) {
					t.Fatal("revoked representation disclosed")
				}
			})
		}
	}
}

package nuget_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
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
		"malformed": `<feed>`,
		"doctype":   `<!DOCTYPE feed [<!ENTITY secret SYSTEM "file:///etc/passwd">]><feed xmlns="` + remoteAtomNS + `">&secret;</feed>`,
		"entity":    `<!DOCTYPE feed [<!ENTITY a "AAAAAAAAAA">]><feed xmlns="` + remoteAtomNS + `">&a;</feed>`,
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

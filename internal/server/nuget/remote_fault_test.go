package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
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

func TestAccRemoteV3MetadataAdmissionAndRecovery(t *testing.T) {
	for name, bad := range map[string]string{"junk": "{} trailing", "null": "null", "wrong-version": `{"version":"2.0.0","resources":[]}`, "relative": `{"version":"3.0.0","resources":[{"@type":"PackageBaseAddress/3.0.0","@id":"/flat/"}]}`, "credential": `{"version":"3.0.0","resources":[{"@type":"PackageBaseAddress/3.0.0","@id":"https://secret:password@evil.example/"}]}`, "ssrf": `{"version":"3.0.0","resources":[{"@type":"PackageBaseAddress/3.0.0","@id":"http://169.254.169.254/latest/"}]}`} {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.up.Set(
				"/v3/index.json",
				fakeupstream.Resource{
					Body:   []byte(bad),
					Header: http.Header{"Content-Type": []string{"application/json"}},
				},
			)
			wantStatus(t, f.do("GET", remoteV3+"index.json", "reader"), 502)
			entries, _, err := f.db.RemoteCache().
				List(t.Context(), "nuget-remote", "", store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Blob.Checksums.SHA256 != "" {
					t.Fatal("invalid metadata cached")
				}
			}
			f.setJSON(
				"/v3/index.json",
				map[string]any{
					"version": "3.0.0",
					"resources": []any{
						map[string]string{
							"@type": "PackageBaseAddress/3.0.0",
							"@id":   f.up.URL + "/flat/",
						},
					},
				},
			)
			wantStatus(t, f.do("GET", remoteV3+"index.json", "reader"), 200)
		})
	}
}

func TestAccRemoteV3TamperAndIdentityNeverPublish(t *testing.T) {
	for _, storeLocally := range []bool{true, false} {
		for _, fault := range []string{"digest", "identity", "crc", "html", "traversal", "encoded-traversal", "directory-bound"} {
			t.Run(fault+string(rune('0'+boolInt(storeLocally))), func(t *testing.T) {
				f := newRemoteFixture(t)
				entries := f.packageDocs("Acme.Mixed", "1.0.0")
				c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
				c.StoreArtifactsLocally = &storeLocally
				c.RemoteRepoChecksumPolicyType = repo.IgnoreAndGenerate
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				body := nupkg("Acme.Mixed", "1.0.0", "")
				switch fault {
				case "digest":
					body = []byte("tampered")
				case "identity":
					body = nupkg("Other.Package", "1.0.0", "")
				case "crc":
					body = bytes.Replace(body, []byte("MZ 1.0.0"), []byte("MZ 9.0.0"), 1)
				case "html":
					body = []byte("<html>upstream login</html>")
				case "traversal", "encoded-traversal":
					name := "../../outside.txt"
					if fault == "encoded-traversal" {
						name = "%2e%2e/%2e%2e/outside.txt"
					}
					var b bytes.Buffer
					original, _ := zip.NewReader(bytes.NewReader(body), int64(len(body)))
					zw := zip.NewWriter(&b)
					for _, entry := range original.File {
						_ = zw.Copy(entry)
					}
					w, _ := zw.Create(name)
					_, _ = io.WriteString(w, "outside")
					_ = zw.Close()
					body = b.Bytes()
				case "directory-bound":
					body[len(body)-10] = 255
					body[len(body)-9] = 255
					body[len(body)-8] = 255
					body[len(body)-7] = 127

				}
				if fault != "digest" {
					sum := sha512.Sum512(body)
					entries[0]["catalogEntry"].(map[string]any)["packageHash"] = base64.StdEncoding.EncodeToString(
						sum[:],
					)
					f.setJSON("/opaque/page/acme.mixed", map[string]any{"items": entries})
				}
				f.up.Set(
					"/objects/acme.mixed-1.0.0.zip",
					fakeupstream.Resource{
						Body:   body,
						Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
					},
				)
				wantStatus(
					t,
					f.do(
						"GET",
						remoteV3+"flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg",
						"reader",
					),
					502,
				)
				_, err := f.db.Items().
					Get(t.Context(), "nuget-remote", true, "acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg")
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("bad package published: %v", err)
				}
			})
		}
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestAccRemoteV3RefreshAndMalformedRegistration(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0")
	path := remoteV3 + "flatcontainer/acme.mixed/index.json"
	wantStatus(t, f.do("GET", path, "reader"), 200)
	f.setJSON("/flat/acme.mixed/index.json", map[string]any{"versions": []string{"2.0.0"}})
	rec := f.do("GET", path, "reader")
	if strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal("TTL ignored")
	}
	f.clock.Advance(6 * time.Second)
	rec = f.do("GET", path, "reader")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal("metadata never refreshed")
	}
	for _, bad := range []any{map[string]any{"items": nil}, map[string]any{"items": []any{map[string]any{"catalogEntry": map[string]any{"id": "Other", "version": "1.0.0"}}}}, map[string]any{"items": []any{map[string]any{"catalogEntry": map[string]any{"id": "Acme.Mixed", "version": "bad"}}}}} {
		f.setJSON("/opaque/page/acme.mixed", bad)
		f.clock.Advance(6 * time.Second)
		wantStatus(
			t,
			f.do("GET", remoteV3+"registration-semver2/acme.mixed/index.json", "reader"),
			502,
		)
	}
}

func TestAccRemoteV3SearchAutocompleteAndConditionalPrivacy(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
	f.packageDocs("Secret.Package", "9.0.0")
	f.setJSON("/query", map[string]any{"totalHits": 200, "data": []any{
		map[string]any{
			"id":             "Acme.Mixed",
			"version":        "1.0.0",
			"totalDownloads": 999,
			"versions": []any{
				map[string]any{"version": "1.0.0", "@id": f.up.URL + "/old"},
				map[string]any{"version": "2.0.0", "@id": f.up.URL + "/secret"},
			},
		},
		map[string]any{
			"id":       "Secret.Package",
			"version":  "9.0.0",
			"versions": []any{map[string]string{"version": "9.0.0"}},
		},
	}})
	rec := f.do("GET", remoteV3+"query?q=Acme&take=10000&semVerLevel=2.0.0", "partial")
	wantStatus(t, rec, 200)
	for _, secret := range []string{"2.0.0", "Secret.Package", "999", f.up.URL} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("leaked %s: %s", secret, rec.Body)
		}
	}
	if !strings.Contains(rec.Body.String(), `"totalHits":1`) {
		t.Fatal(rec.Body)
	}
	wantStatus(
		t,
		f.do(
			"GET",
			remoteV3+"query?q=Acme&take=10000&semVerLevel=2.0.0",
			"partial",
			"If-None-Match",
			rec.Header().Get("ETag"),
		),
		304,
	)
	f.setJSON(
		"/autocomplete",
		map[string]any{"totalHits": 2, "data": []string{"Acme.Mixed", "Secret.Package"}},
	)
	rec = f.do("GET", remoteV3+"autocomplete?q=Acme", "partial")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "Secret.Package") {
		t.Fatal(rec.Body)
	}
	f.setJSON("/autocomplete", map[string]any{"data": []string{"1.0.0", "2.0.0"}})
	rec = f.do("GET", remoteV3+"autocomplete?id=ACME.MIXED", "partial")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal(rec.Body)
	}
}

func TestAccRemoteV3PrivateAuthAndRedirects(t *testing.T) {
	f := newRemoteFixture(
		t,
		fakeupstream.Options{Auth: fakeupstream.Auth{Username: "up", Password: "secret"}},
	)
	wantStatus(t, f.do("GET", remoteV3+"index.json", "reader"), 401)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.Username = "up"
	c.Password = "secret"
	forced := true
	c.ForceNugetAuthentication = &forced
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.do("GET", remoteV3+"index.json", "anonymous"), 401)
	f.setJSON(
		"/redirected/final.json",
		map[string]any{
			"version": "3.0.0",
			"resources": []any{
				map[string]string{"@id": f.up.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
			},
		},
	)
	f.up.Set("/v3/index.json", fakeupstream.Resource{Redirect: "/redirected/final.json"})
	wantStatus(t, f.do("GET", remoteV3+"index.json", "reader"), 200)
	f.clock.Advance(6 * time.Second)
	f.up.Set("/v3/index.json", fakeupstream.Resource{Redirect: "http://169.254.169.254/latest/"})
	wantStatus(t, f.do("GET", remoteV3+"index.json", "reader"), 502)
}

func TestAccRemoteV3RepositorySignaturesAndPinnedCertificate(t *testing.T) {
	f := newRemoteFixture(t, fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}})
	cert := f.up.Certificate()
	digest := sha256.Sum256(cert.Raw)
	hash := hex.EncodeToString(digest[:])
	f.setJSON(
		"/v3/index.json",
		map[string]any{
			"version": "3.0.0",
			"resources": []any{
				map[string]string{
					"@id":   f.up.URL + "/signatures",
					"@type": "RepositorySignatures/5.0.0",
				},
			},
		},
	)
	f.setJSON(
		"/signatures",
		map[string]any{
			"allRepositorySigned": true,
			"signingCertificates": []any{
				map[string]any{
					"contentUrl":   f.up.URL + "/cert.der",
					"fingerprints": map[string]string{"2.16.840.1.101.3.4.2.1": hash},
					"subject":      cert.Subject.String(),
					"issuer":       cert.Issuer.String(),
					"notBefore":    cert.NotBefore.Format(time.RFC3339),
					"notAfter":     cert.NotAfter.Format(time.RFC3339),
				},
			},
		},
	)
	f.up.Set(
		"/cert.der",
		fakeupstream.Resource{
			Body:   cert.Raw,
			Header: http.Header{"Content-Type": []string{"application/pkix-cert"}},
		},
	)
	rec := f.do("GET", remoteV3+"repository-signatures/5.0.0.json", "partial")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), f.up.URL) ||
		!strings.Contains(rec.Body.String(), `"allRepositorySigned":true`) {
		t.Fatal(rec.Body)
	}
	rec = f.do("GET", remoteV3+"repository-certificates/5.0.0/"+hash+".der", "partial")
	wantStatus(t, rec, 200)
	if !bytes.Equal(rec.Body.Bytes(), cert.Raw) {
		t.Fatal("certificate changed")
	}
	wantStatus(t, f.do("GET", remoteV3+"repository-certificates/5.0.0/unknown.der", "reader"), 404)
}

type closeAction struct {
	io.ReadCloser
	after func()
}

func (c *closeAction) Close() error { err := c.ReadCloser.Close(); c.after(); return err }

type backendFunc func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f backendFunc) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func TestAccRemoteV3FinalDisclosedPathFence(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0")
	original := f.svc.HTTP
	revoked := false
	f.svc.HTTP = backendFunc(
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			r, err := original.Read(ctx, q, p)
			if err == nil && strings.Contains(q.Resource.Path, "versions.json") {
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
	rec := f.do("GET", remoteV3+"flatcontainer/acme.mixed/index.json", "reader")
	wantStatus(t, rec, 403)
	if strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal("disclosed version after revocation")
	}
}

func TestAccRemoteV3ReadOnlyAndMissingRouteStatusTable(t *testing.T) {
	f := newRemoteFixture(t)
	f.packageDocs("Acme.Mixed", "1.0.0")
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "", 200},
		{"HEAD", "index.json", 200},
		{"GET", "unknown", 404},
		{"GET", "flatcontainer/x", 404},
		{"GET", "flatcontainer/bad!id/index.json", 404},
		{"GET", "flatcontainer/acme.mixed/bad/acme.mixed.bad.nupkg", 404},
		{"GET", "flatcontainer/acme.mixed/1.0.0/wrong.nupkg", 404},
		{"GET", "registration-semver2/acme.mixed", 404},
		{"GET", "registration-semver2/acme.mixed/bad", 404},
		{"GET", "registration-semver2/acme.mixed/bad.json", 404},
		{"GET", "registration-semver2/acme.mixed/2.0.0.json", 404},
		{"GET", "registration-semver2/acme.mixed/page/bad/1.0.0.json", 404},
		{"GET", "registration-semver2/acme.mixed/other/1.0.0/1.0.0.json", 404},
		{"GET", "registration-semver2/Download/acme.mixed/1.0.0", 200},
		{"GET", "repository-signatures/3.0.0.json", 404},
		{"GET", "repository-signatures/4.7.0.json", 404},
		{"GET", "repository-certificates/5.0.0/unknown.der", 404},
		{"GET", "autocomplete?id=bad!id", 404},
		{"PUT", "symbols", 405},
		{"POST", "query", 405},
		{"DELETE", "flatcontainer/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg", 405},
	} {
		t.Run(
			tc.method+tc.path,
			func(t *testing.T) { wantStatus(t, f.do(tc.method, remoteV3+tc.path, "reader"), tc.status) },
		)
	}
}

func TestAccRemoteV3MinimalFeedNoStoreRangeAndNuspec(t *testing.T) {
	f := newRemoteFixture(t)
	f.setJSON(
		"/v3/index.json",
		map[string]any{
			"version": "3.0.0",
			"resources": []any{
				map[string]string{"@id": f.up.URL + "/flat/", "@type": "PackageBaseAddress/3.0.0"},
			},
		},
	)
	body := nupkg("Acme.Mixed", "1.0.0", "")
	f.up.Set(
		"/flat/acme.mixed/1.0.0/acme.mixed.1.0.0.nupkg",
		fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
		},
	)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	no := false
	c.StoreArtifactsLocally = &no
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	path := remoteV3 + "flatcontainer/acme.mixed/1.0.0/"
	rec := f.do("GET", path+"acme.mixed.1.0.0.nupkg", "reader", "Range", "bytes=0-9")
	wantStatus(t, rec, 206)
	if !bytes.Equal(rec.Body.Bytes(), body[:10]) {
		t.Fatal("archive range changed")
	}
	rec = f.do("HEAD", path+"acme.mixed.1.0.0.nupkg", "reader")
	wantStatus(t, rec, 200)
	if rec.Body.Len() != 0 {
		t.Fatal("HEAD body")
	}
	full := f.do("GET", path+"acme.mixed.nuspec", "reader")
	wantStatus(t, full, 200)
	rec = f.do("GET", path+"acme.mixed.nuspec", "reader", "Range", "bytes=0-9")
	wantStatus(t, rec, 206)
	if !bytes.Equal(rec.Body.Bytes(), full.Body.Bytes()[:10]) {
		t.Fatal("nuspec range applied to archive")
	}
	entries, _, err := f.db.RemoteCache().
		List(t.Context(), "nuget-remote", "", store.Page{Limit: 100})
	if err != nil || len(entries) != 0 {
		t.Fatalf("no-store persisted metadata: %v %d", err, len(entries))
	}
	for _, path := range []string{"query", "autocomplete", "registration/acme.mixed/index.json"} {
		wantStatus(t, f.do("GET", remoteV3+path, "reader"), 404)
	}
}

func TestAccRemoteV3InlineUnlistedAndSemVer2(t *testing.T) {
	f := newRemoteFixture(t)
	entries := f.packageDocs("Acme.Mixed", "1.0.0", "2.0.0-beta.1")
	entries[0]["catalogEntry"].(map[string]any)["listed"] = false
	f.setJSON(
		"/reg/acme.mixed/index.json",
		map[string]any{
			"count": 1,
			"items": []any{
				map[string]any{
					"@id":   f.up.URL + "/unused-page",
					"count": 2,
					"lower": "1.0.0",
					"upper": "2.0.0-beta.1",
					"items": entries,
				},
			},
		},
	)
	rec := f.do("GET", remoteV3+"registration/acme.mixed/index.json", "reader")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "beta.1") ||
		!strings.Contains(rec.Body.String(), `"listed":false`) {
		t.Fatal(rec.Body)
	}
	rec = f.do("GET", remoteV3+"registration-semver2/acme.mixed/index.json", "reader")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "beta.1") {
		t.Fatal(rec.Body)
	}
	if f.up.Count("GET", "/unused-page") != 0 {
		t.Fatal("fetched already inlined page")
	}
	entries[0]["catalogEntry"].(map[string]any)["dependencyGroups"] = []any{
		map[string]any{
			"dependencies": []any{map[string]string{"id": "Other", "range": "[1.0.0-beta.1]"}},
		},
	}
	f.clock.Advance(6 * time.Second)
	f.setJSON(
		"/reg/acme.mixed/index.json",
		map[string]any{
			"count": 1,
			"items": []any{
				map[string]any{
					"@id":   f.up.URL + "/unused-page",
					"count": 2,
					"lower": "1.0.0",
					"upper": "2.0.0-beta.1",
					"items": entries,
				},
			},
		},
	)
	wantStatus(t, f.do("GET", remoteV3+"registration/acme.mixed/index.json", "reader"), 404)
}

// Package support holds server-route test data and request helpers shared by
// independently runnable protocol test packages. Production code must not
// import this package.
package support

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// TestHash keeps server-route authentication tests fast under the race detector.
var TestHash = auth.HashParams{MemoryKiB: 64, Time: 1, Threads: 1}

// CheapHash is the hash of the test principals' shared password, "pw".
var CheapHash = func() []byte {
	h, err := auth.HashPasswordWith(TestHash, "pw")
	if err != nil {
		panic(err)
	}
	return h
}()

func Seed(t *testing.T, db store.Store) {
	t.Helper()
	if _, err := auth.SeedHashed(context.Background(), db, CheapHash, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func Do(
	t *testing.T, h http.Handler, method, path string, headers map[string]string, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func Basic(user, password string) map[string]string {
	return map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)),
	}
}

func Bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func NuGetForm(pkg []byte) string {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.SetBoundary("artifactweave-nuget-boundary")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name=package; filename=package.nupkg`)
	h.Set("Content-Type", "application/octet-stream")
	w, _ := mw.CreatePart(h)
	_, _ = w.Write(pkg)
	_ = mw.Close()
	return "multipart:" + mw.Boundary() + "\n" + buf.String()
}

func NuGetPackage(id, version, more string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ name, body string }{
		{"_rels/.rels", "<Relationships/>"},
		{id + ".nuspec", `<?xml version="1.0" encoding="utf-8"?>` +
			`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">` +
			`<metadata><id>` + id + `</id><version>` + version + `</version>` +
			`<authors>Acme</authors><description>` + id + `</description>` + more +
			`</metadata></package>`},
		{"lib/net8.0/" + id + ".dll", "MZ"},
	} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func NuGetSymbolsPackage(id, version string) []byte {
	return NuGetPackage(id, version,
		`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`)
}

func ArtifactoryError(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("not the Artifactory envelope: %d %q %s",
			rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	return body.Errors[0].Status, body.Errors[0].Message
}

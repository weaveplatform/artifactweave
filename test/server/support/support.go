package support

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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

// Seed creates the built-in principals in db as a new server does (the admin
// user, anonymous, the readers group, the Anything and Any Remote permissions
// and the artifactory-build-info repository), with "pw" as the admin
// password, and fails t if the store refuses them. A store that is already
// seeded is left as it is.
func Seed(t *testing.T, db store.Store) {
	t.Helper()
	if _, err := auth.SeedHashed(context.Background(), db, CheapHash, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Do serves one request to h in process and returns the recorded response.
// It sets each of headers on the request and sends body, or no body at all
// when body is empty.
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

// Basic returns the Authorization header that authenticates as user with
// password over HTTP Basic, in the form Do takes.
func Basic(user, password string) map[string]string {
	return map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password)),
	}
}

// Bearer returns the Authorization header that presents token as a Bearer
// credential, in the form Do takes.
func Bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// NuGetForm returns pkg as the multipart form that nuget push sends, written
// in the route tables' body notation: "multipart:", the boundary, a newline,
// then the form. The suites turn that prefix into the request's
// multipart/form-data Content-Type with the same boundary.
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

// NuGetPackage returns a .nupkg for id at version, as an uncompressed zip: an
// empty _rels/.rels, the nuspec and a stand-in lib/net8.0 assembly. more is
// written inside the nuspec's metadata element, so a test can add elements
// such as packageTypes.
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

// NuGetSymbolsPackage returns a NuGetPackage whose nuspec declares the
// SymbolsPackage package type, which marks a .snupkg symbol package.
func NuGetSymbolsPackage(id, version string) []byte {
	return NuGetPackage(id, version,
		`<packageTypes><packageType name="SymbolsPackage" /></packageTypes>`)
}

// NuGetPortablePDB returns a minimal Portable PDB that the symbol server can
// index. Its symbol key is 030201000504070608090A0B0C0D0E0F followed by
// FFFFFFFF, which stands in for the age in a Portable PDB's key.
func NuGetPortablePDB() []byte {
	b := make([]byte, 120)
	copy(b, "BSJB")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], 1)
	binary.LittleEndian.PutUint32(b[12:], 12)
	copy(b[16:], "PDB v1.0\x00")
	binary.LittleEndian.PutUint16(b[30:], 2)
	binary.LittleEndian.PutUint32(b[32:], 64)
	binary.LittleEndian.PutUint32(b[36:], 32)
	copy(b[40:], "#Pdb\x00")
	binary.LittleEndian.PutUint32(b[48:], 96)
	binary.LittleEndian.PutUint32(b[52:], 24)
	copy(b[56:], "#~\x00")
	for i := range 20 {
		b[64+i] = byte(i)
	}
	return b
}

// NuGetWindowsPDB returns a minimal Windows (MSF 7.00) PDB that the symbol
// server can index. Its symbol key is the GUID of NuGetPortablePDB's key
// followed by the age, 2A.
func NuGetWindowsPDB() []byte {
	b := make([]byte, 6*512)
	copy(b, "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00")
	for off, v := range map[int]uint32{32: 512, 36: 1, 40: 6, 44: 16, 52: 2, 1024: 3, 1536: 2, 1540: 0, 1544: 28, 1548: 5, 2560: 20000404, 2568: 42} {
		binary.LittleEndian.PutUint32(b[off:], v)
	}
	for i := range 16 {
		b[2572+i] = byte(i)
	}
	return b
}

// NuGetPDBPackage returns a symbol package for id at version 1.0.0 that holds
// pdb as lib/net10.0/App.pdb, for the symbol server's indexing tests.
func NuGetPDBPackage(id string, pdb []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ name, body string }{
		{id + ".nuspec", `<package><metadata><id>` + id + `</id><version>1.0.0</version><packageTypes><packageType name="SymbolsPackage" /></packageTypes></metadata></package>`},
		{"lib/net10.0/App.pdb", string(pdb)},
	} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// ArtifactoryError returns the status and message of the error in rec's
// Artifactory error envelope, {"errors":[{"status":...,"message":...}]}. It
// fails t unless rec is application/json and holds exactly one error.
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

package npm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // npm's dist.shasum is SHA-1 by definition
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/npm"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// manifestOf is a package.json for name at version.
func manifestOf(name, version string) string {
	return fmt.Sprintf(`{"name":%q,"version":%q,"description":"published"}`, name, version)
}

// publishDoc is the body npm publish sends for tgz as name at version,
// with tags as its dist-tags. json.Marshal sorts the keys, so
// _attachments comes before versions, the order that makes the tarball
// arrive before the fields naming it.
func publishDoc(name, version string, tgz []byte, tags map[string]string) map[string]any {
	s1 := sha1.Sum(tgz) //nolint:gosec // npm's dist.shasum is SHA-1
	s5 := sha512.Sum512(tgz)
	return map[string]any{
		"_id": name, "name": name, "description": "published",
		"dist-tags": tags, "access": nil,
		"versions": map[string]any{version: map[string]any{
			"_id": name + "@" + version, "name": name, "version": version,
			"dist": map[string]any{
				"shasum":    hex.EncodeToString(s1[:]),
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s5[:]),
				"tarball":   "http://registry.example/" + npmfmt.CanonicalPath(name, version),
			},
		}},
		"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{
			"content_type": "application/octet-stream",
			"data":         base64.StdEncoding.EncodeToString(tgz),
			"length":       len(tgz),
		}},
	}
}

// versionOf is the one version manifest of doc.
func versionOf(doc map[string]any) map[string]any {
	for _, v := range doc["versions"].(map[string]any) {
		return v.(map[string]any)
	}
	return nil
}

// attachmentOf is the one attachment of doc.
func attachmentOf(doc map[string]any) map[string]any {
	for _, a := range doc["_attachments"].(map[string]any) {
		return a.(map[string]any)
	}
	return nil
}

func encodeDoc(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// send makes a request with body as user.
func (f *fixture) send(method, path, user string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if user != "" {
		r.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// publish sends npm publish's body for name at version, as user.
func (f *fixture) publish(
	t *testing.T,
	name, version, user string,
	tags map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	doc := publishDoc(name, version, tarball(manifestOf(name, version)), tags)
	return f.send("PUT", registry+escapeName(name), user, bytes.NewReader(encodeDoc(t, doc)))
}

// escapeName is name as npm puts it in a URL: a scope's slash encoded.
func escapeName(name string) string { return strings.Replace(name, "/", "%2f", 1) }

func (f *fixture) props(t *testing.T, path string) types.Properties {
	t.Helper()
	ctx := context.Background()
	it, err := f.db.Items().Get(ctx, "npm-local", false, path)
	if err != nil {
		t.Fatal(err)
	}
	props, err := f.db.Items().Properties(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	return props
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		!strings.Contains(body.Error, msg) {
		t.Fatalf("got %d %s, want %d with %q", rec.Code, rec.Body, status, msg)
	}
}

func TestAccPublish(t *testing.T) {
	f := newFixture(t)
	rec := f.publish(t, "acme", "1.0.0", "writer", map[string]string{"latest": "1.0.0"})
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	// Packuments are built from the index the npm format keeps, which the
	// publish reached through the item service.
	var doc struct {
		DistTags map[string]string         `json:"dist-tags"`
		Versions map[string]map[string]any `json:"versions"`
	}
	rec = f.do("GET", registry+"acme", "reader")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil ||
		doc.DistTags["latest"] != "1.0.0" || doc.Versions["1.0.0"]["description"] != "published" {
		t.Fatalf("packument: %d %s", rec.Code, rec.Body)
	}
	// latest is stored as 7.x stores it, on the package's first version,
	// by the npm format's rules rather than from the body (decision 0034).
	path := npmfmt.CanonicalPath("acme", "1.0.0")
	props := f.props(t, path)
	if !slices.Equal(props[npmfmt.PropDistTag], []string{"latest"}) ||
		props[npmfmt.PropVersion][0] != "1.0.0" {
		t.Fatalf("properties %v", props)
	}
	it, err := f.db.Items().Get(context.Background(), "npm-local", false, path)
	if err != nil || it.CreatedBy != "writer" {
		t.Fatalf("tarball %+v %v", it, err)
	}

	// Another tag is stored, and moves from the version that had it.
	for _, v := range []string{"2.0.0-beta.1", "2.0.0-beta.2"} {
		wantStatus(
			t,
			f.publish(t, "acme", v, "writer", map[string]string{"beta": v}),
			http.StatusCreated,
		)
	}
	rec = f.do("GET", registry+"acme", "reader")
	if json.Unmarshal(rec.Body.Bytes(), &doc) != nil || doc.DistTags["beta"] != "2.0.0-beta.2" ||
		doc.DistTags["latest"] != "1.0.0" {
		t.Fatalf("dist-tags %v", doc.DistTags)
	}
	if got := f.props(t, npmfmt.CanonicalPath("acme", "2.0.0-beta.1"))[npmfmt.PropDistTag]; got != nil {
		t.Fatalf("beta not moved: %v", got)
	}
	// A body naming a lower version latest moves nothing: 7.x ignores a
	// publish body's latest (RTFACT-8184), and 1.1.0 outranks 1.0.0.
	wantStatus(t, f.publish(t, "acme", "1.1.0", "writer", map[string]string{"latest": "1.0.0"}),
		http.StatusCreated)
	// npm publish --tag next of a higher version leaves latest
	// (RTFACT-24072).
	wantStatus(t, f.publish(t, "acme", "3.0.0", "writer", map[string]string{"next": "3.0.0"}),
		http.StatusCreated)
	rec = f.do("GET", registry+"acme", "reader")
	if json.Unmarshal(rec.Body.Bytes(), &doc) != nil || doc.DistTags["latest"] != "1.1.0" ||
		doc.DistTags["next"] != "3.0.0" {
		t.Fatalf("dist-tags %v", doc.DistTags)
	}
}

func TestAccPublishScoped(t *testing.T) {
	f := newFixture(t)
	for _, url := range []string{registry + "@acme%2fwidget", registry + "@acme/widget"} {
		version := "1.0.0"
		if strings.Contains(url, "/widget") {
			version = "1.0.1"
		}
		doc := publishDoc(
			"@acme/widget",
			version,
			tarball(manifestOf("@acme/widget", version)),
			nil,
		)
		wantStatus(
			t,
			f.send("PUT", url, "writer", bytes.NewReader(encodeDoc(t, doc))),
			http.StatusCreated,
		)
	}
	for _, v := range []string{"1.0.0", "1.0.1"} {
		if p := f.props(
			t,
			"@acme/widget/-/@acme/widget-"+v+".tgz",
		); p[npmfmt.PropName][0] != "@acme/widget" {
			t.Fatalf("%s: %v", v, p)
		}
	}
	wantStatus(t, f.do("GET", registry+"@acme%2fwidget/1.0.1", "reader"), http.StatusOK)
}

// A version is published once: a second publish answers 403 to every
// caller, administrators and those who may delete included (decision 0034),
// and leaves the first tarball.
func TestAccPublishRepublish(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.publish(t, "acme", "1.0.0", "writer", nil), http.StatusCreated)
	first := f.props(t, npmfmt.CanonicalPath("acme", "1.0.0"))
	for _, user := range []string{"writer", "admin"} {
		other := tarball(`{"name":"acme","version":"1.0.0","description":"another"}`)
		doc := publishDoc("acme", "1.0.0", other, nil)
		wantError(
			t,
			f.send("PUT", registry+"acme", user, bytes.NewReader(encodeDoc(t, doc))),
			http.StatusForbidden,
			"You cannot publish over the previously published versions: 1.0.0.",
		)
	}
	// A version spelled differently is the same version.
	doc := publishDoc("acme", "v1.0.0+build", tarball(manifestOf("acme", "1.0.0")), nil)
	wantStatus(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusForbidden)
	if got := f.props(t, npmfmt.CanonicalPath("acme", "1.0.0")); !slices.Equal(
		got[npmfmt.PropDescription], first[npmfmt.PropDescription]) {
		t.Fatalf("replaced: %v", got)
	}
}

func TestAccPublishPermissions(t *testing.T) {
	f := newFixture(t)
	for user, want := range map[string]int{
		"":                  http.StatusUnauthorized,
		"anonymous":         http.StatusUnauthorized,
		"reader":            http.StatusForbidden,
		"nobody":            http.StatusForbidden,
		"narrow":            http.StatusForbidden, // its grant holds no part of acme
		"admin":             http.StatusCreated,
		"writer-not-a-user": http.StatusForbidden,
	} {
		version := "1.0." + fmt.Sprint(len(user))
		wantStatus(t, f.publish(t, "acme", version, user, nil), want)
	}
	// narrow may deploy pkg's 1.x tarballs only. The package's folder lets
	// it start; the canonical path of 2.0.0 refuses it once the body names
	// the version.
	wantStatus(t, f.publish(t, "pkg", "1.0.0", "narrow", nil), http.StatusCreated)
	wantStatus(t, f.publish(t, "pkg", "2.0.0", "narrow", nil), http.StatusForbidden)
}

// A refused publish answers 400 with what is wrong, and stores nothing at
// the canonical path.
func TestAccPublishRefusals(t *testing.T) {
	good := func() map[string]any {
		return publishDoc("acme", "1.0.0", tarball(manifestOf("acme", "1.0.0")), nil)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		msg  string
	}{
		{"name", func(d map[string]any) { d["name"] = "other" }, "the body's name"},
		{"no name", func(d map[string]any) { delete(d, "name") }, "the body's name"},
		{"_id", func(d map[string]any) { d["_id"] = "other" }, "the body's _id"},
		{"no version", func(d map[string]any) { d["versions"] = map[string]any{} }, "exactly one version"},
		{"two versions", func(d map[string]any) {
			d["versions"].(map[string]any)["2.0.0"] = map[string]any{}
		}, "exactly one version"},
		{"no tarball", func(d map[string]any) {
			d["_attachments"] = map[string]any{"acme-1.0.0.sigstore": map[string]any{"data": "e30="}}
		}, "exactly one attachment"},
		{"two tarballs", func(d map[string]any) {
			d["_attachments"].(map[string]any)["acme-2.0.0.tgz"] = map[string]any{"data": "AAAA"}
		}, "exactly one attachment"},
		{"no data", func(d map[string]any) { delete(attachmentOf(d), "data") }, "exactly one attachment"},
		{"length", func(d map[string]any) { attachmentOf(d)["length"] = 7 }, "length 7"},
		{"shasum", func(d map[string]any) {
			versionOf(d)["dist"].(map[string]any)["shasum"] = strings.Repeat("0", 40)
		}, "dist.shasum"},
		{"integrity", func(d map[string]any) {
			versionOf(d)["dist"].(map[string]any)["integrity"] = "sha512-AAAA"
		}, "dist.integrity's sha512"},
		{"manifest", func(d map[string]any) {
			d["versions"] = map[string]any{"1.0.0": "a string"}
		}, "the version's manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			doc := good()
			tc.edit(doc)
			wantError(
				t,
				f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
				http.StatusBadRequest,
				tc.msg,
			)
			if _, err := f.db.Items().Get(context.Background(), "npm-local", false,
				npmfmt.CanonicalPath("acme", "1.0.0")); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("stored: %v", err)
			}
		})
	}
}

// Under server-generated-checksums the checksum policy lets a dist that
// does not match the tarball through, as 7.x does from 7.31.10
// (RTFACT-14280), and the checksums the client declared are recorded as
// the tarball's originals, so the mismatch shows as a REST deploy's does.
// dist.shasum outranks an integrity sha1, and a value too malformed to be
// a checksum is dropped.
func TestAccPublishServerGeneratedChecksums(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c, err := f.db.Repos().Get(ctx, "npm-local")
	if err != nil {
		t.Fatal(err)
	}
	c.ChecksumPolicyType = repo.ServerGeneratedChecksums
	if err := f.db.Repos().Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	shasum := strings.Repeat("ab", sha1.Size)
	other1 := sha1.Sum([]byte("other")) //nolint:gosec // npm's dist.shasum is SHA-1
	other512 := sha512.Sum512([]byte("other"))
	b64 := base64.StdEncoding.EncodeToString
	for _, tc := range []struct {
		version, shasum, integrity string
		want                       types.Checksums
	}{
		{
			"1.0.0", strings.ToUpper(shasum),
			"sha1-" + b64(other1[:]) + " sha512-" + b64(other512[:]) + " sha384-x",
			types.Checksums{SHA1: shasum, SHA512: hex.EncodeToString(other512[:])},
		},
		{
			"1.0.1", "", "sha1-" + b64(other1[:]),
			types.Checksums{SHA1: hex.EncodeToString(other1[:])},
		},
		{"1.0.2", "zz", "sha512-!!! sha256-" + b64([]byte("short")), types.Checksums{}},
	} {
		tgz := tarball(manifestOf("acme", tc.version))
		doc := publishDoc("acme", tc.version, tgz, nil)
		versionOf(doc)["dist"] = map[string]any{"shasum": tc.shasum, "integrity": tc.integrity}
		wantStatus(t, f.send("PUT", registry+"acme", "writer",
			bytes.NewReader(encodeDoc(t, doc))), http.StatusCreated)
		it, err := f.db.Items().Get(ctx, "npm-local", false,
			npmfmt.CanonicalPath("acme", tc.version))
		if err != nil {
			t.Fatal(err)
		}
		// Every write records the computed SHA-256 when none was declared.
		want := tc.want
		want.SHA256 = it.Actual.SHA256
		if it.Original != want {
			t.Errorf("%s: originals %+v, want %+v", tc.version, it.Original, want)
		}
	}
}

// The tarball must be the package and version the body publishes.
func TestAccPublishTarballMismatch(t *testing.T) {
	f := newFixture(t)
	for want, tgz := range map[string][]byte{
		"is other@1.0.0": tarball(manifestOf("other", "1.0.0")),
		"is acme@1.0.1":  tarball(manifestOf("acme", "1.0.1")),
	} {
		doc := publishDoc("acme", "1.0.0", tgz, nil)
		wantError(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
			http.StatusBadRequest, "the tarball's package.json "+want)
	}
	doc := publishDoc("acme", "1.0.0", []byte("not a tarball"), nil)
	wantError(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusBadRequest, "no package tarball")
	doc = publishDoc("acme", "one", tarball(manifestOf("acme", "1.0.0")), nil)
	wantError(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusBadRequest, "the body publishes acme@one")
}

// The body's JSON is read as it streams, so each way it can be malformed
// is caught by the reader rather than by a decoder holding the body.
func TestAccPublishMalformedBodies(t *testing.T) {
	tgz := tarball(manifestOf("acme", "1.0.0"))
	data := base64.StdEncoding.EncodeToString(tgz)
	versions := `"versions":{"1.0.0":{"name":"acme","version":"1.0.0"}}`
	attachment := func(dataJSON string) string {
		return `"_attachments":{"acme-1.0.0.tgz":{"data":` + dataJSON + `}}`
	}
	body := func(parts ...string) string { return `{"name":"acme",` + strings.Join(parts, ",") + `}` }
	f := newFixture(t)
	for _, tc := range []struct{ name, body, msg string }{
		{"empty", "", "it ends early"},
		{"array", "[]", `found '['`},
		{"truncated", body(versions)[:20], "it ends early"},
		{"truncated data", `{"_attachments":{"acme-1.0.0.tgz":{"data":"` + data[:40], "it ends early"},
		{"trailing", body(versions, attachment(`"`+data+`"`)) + " x", "continues after"},
		{"key", `{1:2}`, "an object key is not a string"},
		{"colon", `{"name" "acme"}`, `found '"' where ':'`},
		{"separator", `{"name":"acme" "x":1}`, "after an object's value"},
		{"value", `{"name":}`, "where a value belongs"},
		{"literal", `{"name":nul}`, "a value is not JSON"},
		{"data not a string", body(versions, attachment(`null`)), `found 'n' where '"'`},
		{"data not base64", body(versions, attachment(`"!!!!"`)), "not base64"},
		{"control character", body(versions, attachment("\"AA\x01A\"")), "control character"},
		{"unknown escape", body(versions, attachment(`"AA\qA"`)), `unknown escape \q`},
		{"bad \\u escape", body(versions, attachment(`"AA\u00zz"`)), `malformed \u escape`},
		{"short \\u escape", `{"_attachments":{"a.tgz":{"data":"\u00`, "it ends early"},
		{"short escape", `{"_attachments":{"a.tgz":{"data":"\`, "it ends early"},
		{
			"two data fields", body(versions,
				`"_attachments":{"acme-1.0.0.tgz":{"data":"`+data+`","data":"`+data+`"}}`),
			"two data fields",
		},
	} {
		wantError(t, f.send("PUT", registry+"acme", "writer", strings.NewReader(tc.body)),
			http.StatusBadRequest, tc.msg)
	}
}

// npm sends versions before the attachments, and a client may escape
// the base64's slashes or any character: each is read as JSON reads it.
func TestAccPublishBodyForms(t *testing.T) {
	f := newFixture(t)
	tgz := tarball(manifestOf("acme", "1.0.0"))
	data := base64.StdEncoding.EncodeToString(tgz)
	escaped := strings.ReplaceAll(data, "/", `\/`)
	escaped = `\u00` + hex.EncodeToString([]byte{escaped[0]}) + escaped[1:]
	body := `{
	  "name": "acme", "_id": "acme", "dist-tags": {"next": "1.0.0", "odd": 5},
	  "versions": {"1.0.0": {"name": "acme", "version": "1.0.0", "readme": "a \"quoted\" [readme] {}"}},
	  "_attachments": {
	    "acme-1.0.0.sigstore": {"content_type": "application/json", "data": "e30=", "length": 2},
	    "acme-1.0.0.tgz": {"content_type": "application/octet-stream", "data": "` + escaped + `",
	      "length": ` + fmt.Sprint(len(tgz)) + `}
	  },
	  "access": null, "extra": [1, true, false, null, -2.5e3, {"a": []}]
	}
	`
	wantStatus(t, f.send("PUT", registry+"acme", "writer", strings.NewReader(body)),
		http.StatusCreated)
	path := npmfmt.CanonicalPath("acme", "1.0.0")
	// The package's first version takes latest too, beside the tag the
	// body gives it.
	if got := f.props(t, path)[npmfmt.PropDistTag]; !slices.Equal(
		slices.Sorted(slices.Values(got)), []string{"latest", "next"},
	) {
		t.Fatalf("dist-tags %v", got)
	}
	// Only the tarball is stored.
	children, _, err := f.db.Items().Children(context.Background(), "npm-local", false, "acme/-",
		store.Page{})
	if err != nil || len(children) != 1 {
		t.Fatalf("stored %v %v", children, err)
	}
	// Integrity of several algorithms: those the server computes are
	// checked, others skipped.
	tgz2 := tarball(manifestOf("acme", "1.0.1"))
	doc := publishDoc("acme", "1.0.1", tgz2, nil)
	s1b := sha1.Sum(tgz2) //nolint:gosec // npm's dist.shasum is SHA-1
	s2b := sha256.Sum256(tgz2)
	s5b := sha512.Sum512(tgz2)
	versionOf(doc)["dist"] = map[string]any{"integrity": strings.Join([]string{
		"sha384-unchecked",
		"sha1-" + base64.StdEncoding.EncodeToString(s1b[:]),
		"sha256-" + base64.StdEncoding.EncodeToString(s2b[:]) + "?opt",
		"sha512-" + base64.StdEncoding.EncodeToString(s5b[:]),
	}, " ")}
	wantStatus(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusCreated)
	// No dist at all leaves nothing to check.
	doc = publishDoc("acme", "1.0.2", tarball(manifestOf("acme", "1.0.2")), nil)
	delete(versionOf(doc), "dist")
	delete(attachmentOf(doc), "length")
	wantStatus(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, doc))),
		http.StatusCreated)
}

func TestAccPublishOtherRequests(t *testing.T) {
	f := newFixture(t)
	// npm's other PUTs belong to later tickets, or to no route.
	for _, p := range []string{
		"-", "acme/-rev", "acme/x/y", "-/package/acme/dist-tags",
	} {
		wantStatus(t, f.send("PUT", registry+p, "writer", strings.NewReader("{}")),
			http.StatusMethodNotAllowed)
	}
	wantStatus(t, f.send("PUT", registry+"rich;=v", "writer", strings.NewReader("{}")),
		http.StatusBadRequest)
	// Remote repositories are P9's.
	wantStatus(t, f.send("PUT", "/artifactory/api/npm/npm-remote/acme", "admin",
		strings.NewReader("{}")), http.StatusNotImplemented)
}

func TestAccPublishSizeLimits(t *testing.T) {
	f := newFixture(t)
	doc := encodeDoc(t, publishDoc("acme", "1.0.0", tarball(manifestOf("acme", "1.0.0")), nil))
	small := newFixtureWith(t, npm.Options{MaxPublishSize: int64(len(doc) - 1)})
	// Refused by its declared length before anything is read.
	wantError(t, small.send("PUT", registry+"acme", "writer", bytes.NewReader(doc)),
		http.StatusRequestEntityTooLarge, fmt.Sprintf("the %d bytes", len(doc)-1))
	// A body without a length is refused when it passes the limit.
	r := httptest.NewRequest("PUT", registry+"acme", iotest.OneByteReader(bytes.NewReader(doc)))
	r.ContentLength = -1
	r.Header.Set("X-Test-User", "writer")
	rec := httptest.NewRecorder()
	small.h.ServeHTTP(rec, r)
	wantStatus(t, rec, http.StatusRequestEntityTooLarge)
	// The fields outside the attachments' data are bounded on their own.
	defer npm.SetMaxFields(64)()
	big := publishDoc("acme", "1.0.0", tarball(manifestOf("acme", "1.0.0")), nil)
	big["description"] = strings.Repeat("d", 100)
	wantError(t, f.send("PUT", registry+"acme", "writer", bytes.NewReader(encodeDoc(t, big))),
		http.StatusRequestEntityTooLarge, "besides its attachments' data")
	if _, err := f.db.Items().Get(context.Background(), "npm-local", false,
		npmfmt.CanonicalPath("acme", "1.0.0")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stored: %v", err)
	}
}

// newFixtureWith is newFixture with the npm protocol's options set.
func newFixtureWith(t *testing.T, o npm.Options) *fixture {
	t.Helper()
	f := newFixture(t)
	f.remount(t, o)
	return f
}

// lagging is a blob store that records, as each upload reads its content,
// how far the request body has run ahead of it: produced counts the body's
// bytes read so far.
type lagging struct {
	blob.Store
	produced *atomic.Int64
	maxLag   atomic.Int64
}

func (l *lagging) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	u, err := l.Store.BeginUpload(ctx, opts...)
	return laggingUpload{Upload: u, l: l}, err
}

type laggingUpload struct {
	blob.Upload
	l *lagging
}

func (u laggingUpload) Append(ctx context.Context, r io.Reader) (int64, error) {
	return u.Upload.Append(ctx, &lagReader{r: r, l: u.l})
}

type lagReader struct {
	r        io.Reader
	l        *lagging
	received int64
}

func (r *lagReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.received += int64(n)
	// Base64 is four body bytes for every three stored.
	if lag := r.l.produced.Load() - r.received*4/3; lag > r.l.maxLag.Load() {
		r.l.maxLag.Store(lag)
	}
	return n, err //nolint:wrapcheck // io.Reader contract
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err //nolint:wrapcheck // io.Reader contract
}

// writeLargeTarball writes a package tarball of about size bytes: a
// package.json and a file of zeros, stored without compression so the
// tarball is as large as its content.
func writeLargeTarball(w io.Writer, size int64) error {
	gz, err := gzip.NewWriterLevel(w, gzip.NoCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	manifest := manifestOf("large", "1.0.0")
	for _, f := range []struct {
		name string
		r    io.Reader
		size int64
	}{
		{"package/package.json", strings.NewReader(manifest), int64(len(manifest))},
		{"package/zeros.bin", io.LimitReader(zeros{}, size), size},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o644, Size: f.size, Typeflag: tar.TypeReg,
		}); err != nil {
			return err
		}
		if _, err := io.Copy(tw, f.r); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// A large tarball streams from the body into the blob store: the body is
// never more than a little ahead of what the store has received, which it
// would be by the whole tarball if the body were read before the tarball
// was stored. The fields come after the attachment, as a client may send
// them, so the tarball is stored before its version is known.
func TestAccPublishStreamsLargeTarball(t *testing.T) {
	// Far larger than the reader's 64 KiB buffer, and small enough to run
	// quickly under the race detector.
	const size = 8 << 20
	sha1h, sha512h := sha1.New(), sha512.New() //nolint:gosec // npm's dist.shasum is SHA-1
	var n atomic.Int64
	if err := writeLargeTarball(
		io.MultiWriter(sha1h, sha512h, countingWriter{&n}),
		size,
	); err != nil {
		t.Fatal(err)
	}
	sum := func(h hash.Hash) []byte { return h.Sum(nil) }
	f := newFixture(t)
	var produced atomic.Int64
	l := &lagging{Store: f.items.Blobs, produced: &produced}
	f.items.Blobs = l

	pr, pw := io.Pipe()
	go func() {
		enc := base64.NewEncoder(base64.StdEncoding, pw)
		_, _ = io.WriteString(pw, `{"_attachments":{"large-1.0.0.tgz":{"data":"`)
		err := writeLargeTarball(enc, size)
		if err == nil {
			err = enc.Close()
		}
		if err == nil {
			_, err = fmt.Fprintf(
				pw,
				`","length":%d}},"name":"large","versions":{"1.0.0":`+
					`{"dist":{"shasum":%q,"integrity":%q}}}}`,
				n.Load(),
				hex.EncodeToString(
					sum(sha1h),
				),
				"sha512-"+base64.StdEncoding.EncodeToString(sum(sha512h)),
			)
		}
		_ = pw.CloseWithError(err)
	}()
	rec := f.send("PUT", registry+"large", "writer", countingReader{r: pr, n: &produced})
	wantStatus(t, rec, http.StatusCreated)
	// The body may lead by the reader's buffer and one blob store write.
	if lag := l.maxLag.Load(); lag > 1<<20 {
		t.Fatalf("the body ran %d bytes ahead of the blob store", lag)
	}
	if p := f.props(
		t,
		npmfmt.CanonicalPath("large", "1.0.0"),
	); p[npmfmt.PropVersion][0] != "1.0.0" {
		t.Fatalf("not indexed: %v", p)
	}
}

type countingWriter struct{ n *atomic.Int64 }

func (c countingWriter) Write(p []byte) (int, error) { c.n.Add(int64(len(p))); return len(p), nil }

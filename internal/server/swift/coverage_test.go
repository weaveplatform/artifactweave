package swift_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type publishPart struct {
	name, encoding string
	body           []byte
}

func multipartRelease(t *testing.T, parts ...publishPart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, part := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+part.name+`"`)
		if part.name == "source-archive" {
			header.Set("Content-Type", "application/zip")
		}
		if part.encoding != "" {
			header.Set("Content-Transfer-Encoding", part.encoding)
		}
		p, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Write(part.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func TestUnitSwiftPublicationRejectsInvalidFormsAndDeniedWriter(t *testing.T) {
	good := archive(t, "Widget/")
	for _, tc := range []struct {
		name   string
		parts  []publishPart
		status int
	}{
		{"missing archive", []publishPart{{name: "metadata", body: []byte(`{}`)}}, 400},
		{"unknown part", []publishPart{{name: "unexpected", body: []byte("x")}}, 400},
		{"duplicate archive", []publishPart{
			{name: "source-archive", body: good}, {name: "source-archive", body: good},
		}, 400},
		{"invalid metadata", []publishPart{
			{name: "source-archive", body: good}, {name: "metadata", body: []byte(`{"broken":`)},
		}, 400},
		{"oversized metadata", []publishPart{
			{name: "source-archive", body: good}, {name: "metadata", body: bytes.Repeat([]byte("a"), 64<<10+1)},
		}, 413},
		{"unsupported transfer encoding", []publishPart{
			{name: "source-archive", encoding: "quoted-printable", body: good},
		}, 400},
		{"archive signature", []publishPart{
			{name: "source-archive", body: good}, {name: "source-archive-signature", body: []byte("unsigned")},
		}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			body, contentType := multipartRelease(t, tc.parts...)
			rec := f.do(http.MethodPut, base+"acme/Widget/1.0.0", body, contentType, "admin")
			if rec.Code != tc.status {
				t.Fatalf("publish form: %d %s", rec.Code, rec.Body)
			}
			if rec := f.do(
				http.MethodGet,
				base+"acme/Widget/1.0.0",
				nil,
				"",
				"admin",
			); rec.Code != http.StatusNotFound {
				t.Fatalf("rejected form published a release: %d %s", rec.Code, rec.Body)
			}
		})
	}
	f := newFixture(t, repo.Local)
	body, contentType := multipartRelease(t, publishPart{name: "source-archive", body: good})
	if rec := f.do(
		http.MethodPut,
		base+"acme/Widget/1.0.0",
		body,
		contentType,
		"reader",
	); rec.Code != http.StatusForbidden {
		t.Fatalf("unprivileged publish: %d %s", rec.Code, rec.Body)
	}
	body, contentType = multipartRelease(t, publishPart{name: "source-archive", body: good})
	r := httptest.NewRequest(http.MethodPut, base+"acme/Widget/1.0.0", body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-User", "admin")
	r.Header.Set("X-Swift-Package-Signature-Format", "cms-1.0.0")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("signed archive without validation: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget/1.0.0",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("denied writer or signed archive published a release: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitSwiftManifestAlternatesAndQueryValidation(t *testing.T) {
	f := newFixture(t, repo.Local)
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	for name, content := range map[string]string{
		"Widget/Package.swift":               manifest,
		"Widget/Package@swift-5.9.swift":     "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"WidgetAlt\")\n",
		"Widget/Sources/Widget/Widget.swift": "public struct Widget {}\n",
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	formBody, contentType := multipartRelease(
		t,
		publishPart{name: "source-archive", body: body.Bytes()},
	)
	url := base + "acme/Widget/1.0.0"
	if rec := f.do(
		http.MethodPut,
		url,
		formBody,
		contentType,
		"admin",
	); rec.Code != http.StatusCreated {
		t.Fatalf("publish alternate manifest: %d %s", rec.Code, rec.Body)
	}
	info := f.do(http.MethodGet, url, nil, "", "admin")
	var release struct {
		PublishedAt string `json:"publishedAt"` //nolint:tagliatelle // Swift registry wire field
	}
	if info.Code != http.StatusOK || json.Unmarshal(info.Body.Bytes(), &release) != nil {
		t.Fatalf("release metadata: %d %s", info.Code, info.Body)
	}
	if _, err := time.Parse(
		time.RFC3339,
		release.PublishedAt,
	); err != nil ||
		strings.Contains(release.PublishedAt, ".") {
		t.Fatalf("SwiftPM-compatible publication time: %q %v", release.PublishedAt, err)
	}
	for _, tc := range []struct {
		query  string
		status int
		body   string
	}{
		{"", 200, "import PackageDescription"},
		{"?swift-version=5.9", 200, "WidgetAlt"},
		{"?swift-version=6.0", 303, ""},
		{"?swift-version=bad", 400, ""},
		{"?swift-version=5.9&extra=1", 400, ""},
	} {
		rec := f.do(http.MethodGet, url+"/Package.swift"+tc.query, nil, "", "admin")
		if rec.Code != tc.status || tc.body != "" && !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("manifest %q: %d %s", tc.query, rec.Code, rec.Body)
		}
		if tc.status == http.StatusSeeOther &&
			!strings.HasSuffix(rec.Header().Get("Location"), "/Package.swift") {
			t.Fatalf("manifest redirect target: %q", rec.Header().Get("Location"))
		}
	}
	if rec := f.do(
		http.MethodGet,
		url+".zip",
		nil,
		"",
		"reader",
	); rec.Code != http.StatusForbidden {
		t.Fatalf("unprivileged archive read: %d %s", rec.Code, rec.Body)
	}
	encoded := base64.StdEncoding.EncodeToString(archive(t, "EncodedWidget/"))
	encodedForm, encodedType := multipartRelease(t, publishPart{
		name: "source-archive", encoding: "base64", body: []byte(encoded),
	})
	if rec := f.do(
		http.MethodPut,
		base+"acme/Widget/2.0.0",
		encodedForm,
		encodedType,
		"admin",
	); rec.Code != http.StatusCreated {
		t.Fatalf("base64 archive publication: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitSwiftRemoteRefusesBadRegistryRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, request, media, body string
	}{
		{"invalid release list", "/swift/acme/Widget", base + "acme/Widget", "application/json", `{"releases":`},
		{"invalid listed version", "/swift/acme/Widget", base + "acme/Widget", "application/json", `{"releases":{"not-semver":{}}}`},
		{"wrong list media type", "/swift/acme/Widget", base + "acme/Widget", "text/plain", `{"releases":{"1.0.0":{}}}`},
		{"release identity mismatch", "/swift/acme/Widget/1.0.0", base + "acme/Widget/1.0.0", "application/json", `{"id":"other.Widget","version":"1.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"` + strings.Repeat("0", 64) + `"}]}`},
		{"release version mismatch", "/swift/acme/Widget/1.0.0", base + "acme/Widget/1.0.0", "application/json", `{"id":"acme.Widget","version":"2.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"` + strings.Repeat("0", 64) + `"}]}`},
		{"invalid manifest", "/swift/acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0/Package.swift", "text/x-swift", `print("not a package manifest")`},
		{"oversized manifest", "/swift/acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0/Package.swift", "text/x-swift", strings.Repeat("x", 1<<20+1)},
		{"wrong manifest media type", "/swift/acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0/Package.swift", "application/json", manifest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, "correct")
			f.up.Set(tc.upstream, upstreamResource(tc.media, []byte(tc.body)))
			rec := f.do(http.MethodGet, tc.request, nil, "", "admin")
			if rec.Code == http.StatusOK {
				t.Fatalf("malformed upstream representation was served: %d %s", rec.Code, rec.Body)
			}
		})
	}
	t.Run("corrupt archive with matching metadata checksum", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.seed(t, "correct")
		corrupt := []byte("not a ZIP")
		checksum := fmt.Sprintf("%x", sha256.Sum256(corrupt))
		f.up.Set(
			"/swift/acme/Widget/1.0.0",
			upstreamResource("application/json", []byte(fmt.Sprintf(
				`{"id":"acme.Widget","version":"1.0.0","resources":[{"name":"source-archive","type":"application/zip","checksum":"%s"}]}`,
				checksum,
			))),
		)
		f.up.Set("/swift/acme/Widget/1.0.0.zip", upstreamResource("application/zip", corrupt))
		rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0.zip", nil, "", "admin")
		if rec.Code == http.StatusOK {
			t.Fatal("checksum-matching but malformed ZIP was served")
		}
	})
	t.Run("unavailable upstream", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.seed(t, "correct")
		f.up.Set("/swift/acme/Widget", fakeupstream.Resource{Status: http.StatusServiceUnavailable})
		rec := f.do(http.MethodGet, base+"acme/Widget", nil, "", "admin")
		if rec.Code == http.StatusOK {
			t.Fatal("unavailable upstream produced a successful release list")
		}
	})
}

func TestUnitSwiftVirtualRejectsInvalidMembersAndUpstreamFailure(t *testing.T) {
	const prefix = "/artifactory/api/swift/virtual/acme/Widget"
	for _, tc := range []struct {
		name        string
		members     []string
		breakSource bool
		status      int
	}{
		{"duplicate member", []string{"packages", "packages"}, false, http.StatusBadRequest},
		{"missing member", []string{"missing"}, false, http.StatusNotFound},
		{"nested virtual member", []string{"nested"}, false, http.StatusBadRequest},
		{"failed remote member", []string{"packages"}, true, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, "correct")
			if tc.name == "nested virtual member" {
				nested := *repo.Defaults(&repo.Config{
					Key: "nested", Class: repo.Virtual, PackageType: repo.Swift,
					Repositories: []string{"packages"},
				})
				if err := f.db.Repos().Create(t.Context(), nested); err != nil {
					t.Fatal(err)
				}
			}
			virtual := *repo.Defaults(&repo.Config{
				Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
				Repositories: tc.members,
			})
			if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
				t.Fatal(err)
			}
			if tc.breakSource {
				f.up.Set(
					"/swift/acme/Widget",
					fakeupstream.Resource{Status: http.StatusServiceUnavailable},
				)
			}
			rec := f.do(http.MethodGet, prefix, nil, "", "admin")
			if rec.Code != tc.status {
				t.Fatalf("virtual list: %d %s", rec.Code, rec.Body)
			}
			if tc.name == "missing member" {
				rec = f.do(http.MethodGet, prefix+"/1.0.0.zip", nil, "", "admin")
				if rec.Code != http.StatusNotFound {
					t.Fatalf("missing virtual archive: %d %s", rec.Code, rec.Body)
				}
			}
			if tc.name == "failed remote member" {
				rec = f.do(http.MethodGet, prefix, nil, "", "reader")
				if rec.Code != http.StatusForbidden {
					t.Fatalf("unprivileged virtual read: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
	t.Run("failed release metadata member", func(t *testing.T) {
		f := newRemoteFixture(t)
		f.seed(t, "correct")
		virtual := *repo.Defaults(&repo.Config{
			Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
			Repositories: []string{"packages"},
		})
		if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
			t.Fatal(err)
		}
		f.up.Set(
			"/swift/acme/Widget/1.0.0",
			upstreamResource("application/json", []byte(`{"broken":`)),
		)
		rec := f.do(http.MethodGet,
			"/artifactory/api/swift/virtual/acme/Widget/1.0.0", nil, "", "admin")
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("failed virtual member metadata: %d %s", rec.Code, rec.Body)
		}
	})
}

func TestUnitSwiftLocalDoesNotAdvertiseStaleOrUnreadableReleases(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, version := range []string{"1.0.0", "2.0.0"} {
		body, contentType := multipartRelease(t, publishPart{
			name: "source-archive", body: archive(t, "Widget/"),
		})
		rec := f.do(http.MethodPut, base+"acme/Widget/"+version, body, contentType, "admin")
		if rec.Code != http.StatusCreated {
			t.Fatalf("publish %s: %d %s", version, rec.Code, rec.Body)
		}
	}
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "swift-single-version", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{
					"packages": {Include: []string{"acme/Widget/Widget-1.0.0.zip"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodGet, base+"acme/Widget", nil, "", "reader")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"1.0.0"`) ||
		strings.Contains(rec.Body.String(), `"2.0.0"`) {
		t.Fatalf("local list exposed unreadable release: %d %s", rec.Code, rec.Body)
	}
	path := "acme/Widget/Widget-1.0.0.zip"
	it, err := f.db.Items().Get(t.Context(), "packages", false, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Items().SetProperties(t.Context(), it.ID,
		types.Properties{"swift.metadata": {`{"broken":`}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, base+"acme/Widget/1.0.0", nil, "", "admin")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt stored metadata was served: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().Delete(t.Context(), "packages", false, path); err != nil {
		t.Fatal(err)
	}
	rec = f.do(http.MethodGet, base+"acme/Widget", nil, "", "reader")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), `"1.0.0"`) {
		t.Fatalf("stale or unreadable release was listed: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget/1.0.0",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("stale release metadata was served: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitSwiftRemoteReleaseMetadataRequiresOneValidArchiveResource(t *testing.T) {
	for _, tc := range []struct {
		name, resources string
	}{
		{"missing archive", `[]`},
		{"wrong resource type", `[{"name":"source-archive","type":"text/plain","checksum":"` + strings.Repeat("a", 64) + `"}]`},
		{"nonhex archive checksum", `[{"name":"source-archive","type":"application/zip","checksum":"` + strings.Repeat("z", 64) + `"}]`},
		{"duplicate archive resources", `[{"name":"source-archive","type":"application/zip","checksum":"` + strings.Repeat("a", 64) + `"},{"name":"source-archive","type":"application/zip","checksum":"` + strings.Repeat("a", 64) + `"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, "correct")
			f.up.Set("/swift/acme/Widget/1.0.0", upstreamResource("application/json", []byte(
				`{"id":"acme.Widget","version":"1.0.0","resources":`+tc.resources+`}`)))
			rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0", nil, "", "admin")
			if rec.Code == http.StatusOK {
				t.Fatalf("invalid release metadata was served: %d %s", rec.Code, rec.Body)
			}
		})
	}
	t.Run("unrelated resource does not replace source archive", func(t *testing.T) {
		f := newRemoteFixture(t)
		zipBytes := f.seed(t, "correct")
		checksum := fmt.Sprintf("%x", sha256.Sum256(zipBytes))
		f.up.Set(
			"/swift/acme/Widget/1.0.0",
			upstreamResource("application/json", []byte(fmt.Sprintf(
				`{"id":"acme.Widget","version":"1.0.0","resources":[{"name":"documentation","type":"text/plain"},{"name":"source-archive","type":"application/zip","checksum":"%s"}]}`,
				checksum,
			))),
		)
		rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0", nil, "", "admin")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), checksum) {
			t.Fatalf("valid source archive hidden by unrelated resource: %d %s", rec.Code, rec.Body)
		}
	})
}

func TestUnitSwiftVirtualKeepsMetadataAndManifestOnVirtualURL(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed(t, "correct")
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
		Repositories: []string{"packages"},
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	prefix := "/artifactory/api/swift/virtual/acme/Widget/1.0.0"
	for _, tc := range []struct {
		path, contains string
	}{
		{prefix, `"id":"acme.Widget"`},
		{prefix + "/Package.swift", "import PackageDescription"},
	} {
		rec := f.do(http.MethodGet, tc.path, nil, "", "admin")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Fatalf("virtual member metadata %s: %d %s", tc.path, rec.Code, rec.Body)
		}
		if rec := f.do(
			http.MethodHead,
			tc.path,
			nil,
			"",
			"admin",
		); rec.Code != http.StatusOK ||
			rec.Body.Len() != 0 {
			t.Fatalf("virtual member HEAD %s: %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	if rec := f.do(
		http.MethodGet,
		prefix+"/Package.swift?swift-version=bad",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid virtual manifest query: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		prefix+"?unknown=1",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown virtual release query: %d %s", rec.Code, rec.Body)
	}
	body, contentType := multipartRelease(
		t,
		publishPart{name: "source-archive", body: archive(t, "Widget/")},
	)
	if rec := f.do(
		http.MethodPut,
		prefix,
		body,
		contentType,
		"admin",
	); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("publication to virtual repository: %d %s", rec.Code, rec.Body)
	}
	body, contentType = multipartRelease(
		t,
		publishPart{name: "source-archive", body: archive(t, "Widget/")},
	)
	if rec := f.do(
		http.MethodPut,
		base+"acme/Widget/2.0.0",
		body,
		contentType,
		"admin",
	); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("publication to remote repository: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitSwiftRemoteFiltersAllDeniedVersionsAndNegotiatesManifest(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed(t, "correct")
	f.up.Set("/swift/acme/Widget", upstreamResource("application/json",
		[]byte(`{"releases":{"2.0.0":{}}}`)))
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget",
		nil,
		"",
		"reader",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("release list disclosed only-denied versions: %d %s", rec.Code, rec.Body)
	}
	f2 := newRemoteFixture(t)
	f2.seed(t, "correct")
	rec := f2.do(http.MethodGet,
		base+"acme/Widget/1.0.0/Package.swift?swift-version=5.9", nil, "", "admin")
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "import PackageDescription") {
		t.Fatalf("versioned manifest negotiation: %d %s", rec.Code, rec.Body)
	}
}

func TestUnitSwiftVirtualSkipsAbsentAndUnreadableMembers(t *testing.T) {
	f := newRemoteFixture(t)
	f.seed(t, "correct")
	local := *repo.Defaults(&repo.Config{Key: "local", Class: repo.Local, PackageType: repo.Swift})
	if err := f.db.Repos().Create(t.Context(), local); err != nil {
		t.Fatal(err)
	}
	virtual := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
		Repositories: []string{"local", "packages"},
	})
	if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	prefix := "/artifactory/api/swift/virtual/acme/Widget"
	for _, suffix := range []string{"", "/1.0.0", "/1.0.0/Package.swift"} {
		rec := f.do(http.MethodGet, prefix+suffix, nil, "", "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf(
				"virtual failed to skip absent local member %s: %d %s",
				suffix,
				rec.Code,
				rec.Body,
			)
		}
	}
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "swift-virtual-only", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{"virtual-reader": types.ActionRead},
				},
				Targets: map[string]types.Target{
					"virtual": {Include: []string{"**"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/1.0.0", "/1.0.0.zip"} {
		rec := f.do(http.MethodGet, prefix+suffix, nil, "", "virtual-reader")
		if rec.Code == http.StatusOK {
			t.Fatalf(
				"virtual read disclosed unreadable member %s: %d %s",
				suffix,
				rec.Code,
				rec.Body,
			)
		}
	}
}

func TestUnitSwiftMissingOrFailedUpstreamResourcesStayUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, request string
		status                  int
	}{
		{"missing release list", "/swift/acme/Widget", base + "acme/Widget", 0},
		{"missing release metadata", "/swift/acme/Widget/1.0.0", base + "acme/Widget/1.0.0", 0},
		{"missing manifest", "/swift/acme/Widget/1.0.0/Package.swift", base + "acme/Widget/1.0.0/Package.swift", 0},
		{"missing archive", "/swift/acme/Widget/1.0.0.zip", base + "acme/Widget/1.0.0.zip", 0},
		{"failed archive", "/swift/acme/Widget/1.0.0.zip", base + "acme/Widget/1.0.0.zip", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, "correct")
			if tc.status == 0 {
				f.up.Delete(tc.upstream)
			} else {
				f.up.Set(tc.upstream, fakeupstream.Resource{Status: tc.status})
			}
			rec := f.do(http.MethodGet, tc.request, nil, "", "admin")
			if rec.Code == http.StatusOK {
				t.Fatalf("unavailable upstream resource was served: %d %s", rec.Code, rec.Body)
			}
		})
	}
	for _, status := range []int{0, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("virtual archive upstream status %d", status), func(t *testing.T) {
			f := newRemoteFixture(t)
			f.seed(t, "correct")
			virtual := *repo.Defaults(&repo.Config{
				Key: "virtual", Class: repo.Virtual, PackageType: repo.Swift,
				Repositories: []string{"packages"},
			})
			if err := f.db.Repos().Create(t.Context(), virtual); err != nil {
				t.Fatal(err)
			}
			if status == 0 {
				f.up.Delete("/swift/acme/Widget/1.0.0.zip")
			} else {
				f.up.Set("/swift/acme/Widget/1.0.0.zip", fakeupstream.Resource{Status: status})
			}
			rec := f.do(
				http.MethodGet,
				"/artifactory/api/swift/virtual/acme/Widget/1.0.0.zip",
				nil,
				"",
				"admin",
			)
			if rec.Code == http.StatusOK {
				t.Fatalf("virtual served unavailable member archive: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitSwiftLocalManifestRejectsMissingOrCorruptStoredArchive(t *testing.T) {
	for _, kind := range []string{"missing blob", "corrupt blob"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			body, contentType := multipartRelease(t, publishPart{
				name: "source-archive", body: archive(t, "Widget/"),
			})
			if rec := f.do(
				http.MethodPut,
				base+"acme/Widget/1.0.0",
				body,
				contentType,
				"admin",
			); rec.Code != http.StatusCreated {
				t.Fatalf("publish: %d %s", rec.Code, rec.Body)
			}
			it, err := f.db.Items().Get(t.Context(), "packages", false,
				"acme/Widget/Widget-1.0.0.zip")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing blob" {
				if err := f.items.Blobs.Delete(t.Context(), it.Actual.SHA256); err != nil {
					t.Fatal(err)
				}
			} else {
				upload, err := f.items.Blobs.BeginUpload(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := upload.Append(
					t.Context(),
					strings.NewReader("invalid ZIP"),
				); err != nil {
					t.Fatal(err)
				}
				info, err := upload.Commit(t.Context(), blob.Digests{})
				if err != nil {
					t.Fatal(err)
				}
				it.Actual.SHA256, it.Size = info.Digests.SHA256, info.Size
				if _, err := f.db.Items().Put(t.Context(), it); err != nil {
					t.Fatal(err)
				}
			}
			rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0/Package.swift", nil, "", "admin")
			if rec.Code == http.StatusOK ||
				strings.Contains(rec.Body.String(), "import PackageDescription") {
				t.Fatalf("manifest served from unavailable archive: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestUnitSwiftRegistryReadAliasesAndAuthorization(t *testing.T) {
	f := newFixture(t, repo.Local)
	body, contentType := multipartRelease(t, publishPart{
		name: "source-archive", body: archive(t, "Widget/"),
	})
	if rec := f.do(
		http.MethodPut,
		base+"acme/Widget/1.0.0",
		body,
		contentType,
		"admin",
	); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{
		base + "acme/Widget/not-semver", base + "acme/Widget/1.0.0/other",
	} {
		if rec := f.do(http.MethodGet, path, nil, "", "admin"); rec.Code == http.StatusOK {
			t.Fatalf("invalid Swift registry resource was served: %s %d", path, rec.Code)
		}
	}
	if rec := f.do(
		http.MethodGet,
		base+"acme/Widget/1.0.0.json",
		nil,
		"",
		"admin",
	); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"version":"1.0.0"`) {
		t.Fatalf("release JSON alias: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(
		http.MethodGet,
		base+"identifiers?url=https%3A%2F%2Fexample.com",
		nil,
		"",
		"reader",
	); rec.Code != http.StatusForbidden {
		t.Fatalf("unprivileged identifier lookup: %d %s", rec.Code, rec.Body)
	}
}

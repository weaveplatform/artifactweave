package swifthttp_test

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	swiftfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/swift"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	problemType = "application/problem+json; charset=utf-8"
	swiftLocal  = "/artifactory/api/swift/swift-local/"
)

// swiftWorld is the production router over harness.NewItemWorld, without
// anonymous access, with the Swift local repository swift-local. Its errors
// pass through the server's middleware as well as the protocol's handlers.
func swiftWorld(t *testing.T, tune ...harness.Tune) *harness.ItemWorld {
	t.Helper()
	quiet := func(c *harness.Config) { c.Logger = slog.New(slog.DiscardHandler) }
	w := harness.NewItemWorld(t, false, append([]harness.Tune{quiet}, tune...)...)
	c := *repo.Defaults(&repo.Config{Key: "swift-local", Class: repo.Local, PackageType: repo.Swift})
	if err := w.DB.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return w
}

// problemHeaders is the header set of a Swift registry error: problem JSON
// with Content-Version 1, and the challenge and Retry-After when given.
func problemHeaders(challenge, retryAfter string) http.Header {
	h := http.Header{"Content-Type": {problemType}, "Content-Version": {"1"}}
	if challenge != "" {
		h.Set("WWW-Authenticate", challenge)
	}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return h
}

// errorHeaders are the headers an error's envelope sets. The server adds
// others, such as X-Request-Id and the limiter's RateLimit, that no
// envelope chooses.
var errorHeaders = []string{"Content-Type", "Content-Version", "WWW-Authenticate", "Retry-After"}

// wantAnswer fails t unless rec has the status and body given, the body
// compared with its trailing newline, and each of errorHeaders as in
// headers, absent where headers has none.
func wantAnswer(
	t *testing.T,
	name string,
	rec *httptest.ResponseRecorder,
	status int,
	headers http.Header,
	body string,
) {
	t.Helper()
	ok := rec.Code == status && rec.Body.String() == body+"\n"
	for _, h := range errorHeaders {
		ok = ok && slices.Equal(rec.Header().Values(h), headers.Values(h))
	}
	if !ok {
		t.Errorf("%s: %d %v %s, want %d %v %s",
			name, rec.Code, rec.Header(), rec.Body, status, headers, body)
	}
}

// TestAccSwiftErrorsCarryContentVersion: every Swift registry error carries
// Content-Version 1, the handlers' and the middleware's alike, through the
// production router (#1278). The reindex operation beside the protocol keeps
// Artifactory's envelope, so it gains no Content-Version.
func TestAccSwiftErrorsCarryContentVersion(t *testing.T) {
	w := swiftWorld(t)
	// 15 seconds into a minute, so the limiter's Retry-After is 45.
	clock := func() time.Time { return time.Date(2026, 10, 28, 12, 0, 15, 0, time.UTC) }
	limited := swiftWorld(t, func(c *harness.Config) {
		o := apilimit.Default()
		o.PrincipalPerMinute = 1
		budgets := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		var err error
		c.RequestLimits, err = apilimit.New(budgets, o, clock)
		if err != nil {
			t.Fatal(err)
		}
	})
	// The minute's one request.
	_ = limited.Do(http.MethodGet, "/artifactory/api/system/version", "admin", "")
	form, contentType := form(t, archive(t, "Widget/"))
	for _, c := range []struct {
		name    string
		rec     *httptest.ResponseRecorder
		status  int
		headers http.Header
		body    string
	}{
		{
			"handler 404",
			w.Do(http.MethodGet, swiftLocal+"acme/Widget", "admin", ""),
			http.StatusNotFound, problemHeaders("", ""),
			`{"detail":"Swift package not found","status":404,"title":"Not Found"}`,
		},
		{
			"middleware 401",
			w.Do(http.MethodGet, swiftLocal+"acme/Widget", "", ""),
			http.StatusUnauthorized, problemHeaders(apierror.BasicChallenge, ""),
			`{"detail":"Authentication is required","status":401,"title":"Unauthorized"}`,
		},
		{
			"handler 403",
			w.Do(http.MethodPut, swiftLocal+"acme/Widget/1.0.0", "deployer", form.String(),
				"Content-Type", contentType),
			http.StatusForbidden, problemHeaders("", ""),
			`{"detail":"Forbidden","status":403,"title":"Forbidden"}`,
		},
		{
			"limiter 429",
			limited.Do(http.MethodGet, swiftLocal+"acme/Widget", "admin", ""),
			http.StatusTooManyRequests, problemHeaders("", "45"),
			`{"detail":"API request limit exceeded","status":429,"title":"Too Many Requests"}`,
		},
		{
			"reindex 401, kept",
			w.Do(http.MethodPost, swiftLocal+"reindex", "", ""),
			http.StatusUnauthorized,
			http.Header{
				"Content-Type":     {"application/json"},
				"Www-Authenticate": {apierror.BasicChallenge},
			},
			`{"errors":[{"message":"Authentication is required","status":401}]}`,
		},
	} {
		wantAnswer(t, c.name, c.rec, c.status, c.headers, c.body)
	}
}

// TestUnitSwiftPublishRefusesAnEmptyArchiveAsInvalid: an empty source
// archive, sent as binary or base64, answers 422 "invalid Swift source
// archive", as an archive that is not a ZIP does, not 413 (#1235).
func TestUnitSwiftPublishRefusesAnEmptyArchiveAsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		part publishPart
	}{
		{"empty", publishPart{name: "source-archive"}},
		{"empty base64", publishPart{name: "source-archive", encoding: "base64"}},
		{"not a ZIP", publishPart{name: "source-archive", body: []byte("not a zip")}},
	} {
		f := newFixture(t, repo.Local)
		body, contentType := multipartRelease(t, tc.part)
		wantAnswer(t, tc.name,
			f.do(http.MethodPut, base+"acme/Widget/1.0.0", body, contentType, "admin"),
			http.StatusUnprocessableEntity, problemHeaders("", ""),
			`{"detail":"invalid Swift source archive","status":422,"title":"Unprocessable Entity"}`)
		rec := f.do(http.MethodGet, base+"acme/Widget/1.0.0", nil, "", "admin")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: refused archive published a release: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
}

// zeroRelease is a publish form whose source archive is size zero bytes,
// streamed rather than built in memory because the archives at the limit
// are 512 MiB.
func zeroRelease(t *testing.T, size int64) (io.Reader, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if _, err := w.CreateFormFile("source-archive", "Widget-1.0.0.zip"); err != nil {
		t.Fatal(err)
	}
	head := bytes.Clone(b.Bytes())
	b.Reset()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return io.MultiReader(bytes.NewReader(head), io.LimitReader(zeros{}, size), &b),
		w.FormDataContentType()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestAccSwiftPublishRefusesOnlyAnArchiveOverTheLimitAsTooLarge: an archive
// of exactly MaxArchiveBytes is still read as an archive, so content that is
// not one answers 422, and one byte more answers 413. It streams 1 GiB
// through the handler, so it runs once, in the acceptance lane.
func TestAccSwiftPublishRefusesOnlyAnArchiveOverTheLimitAsTooLarge(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, tc := range []struct {
		size   int64
		status int
		body   string
	}{
		{
			swiftfmt.MaxArchiveBytes, http.StatusUnprocessableEntity,
			`{"detail":"invalid Swift source archive","status":422,"title":"Unprocessable Entity"}`,
		},
		{
			swiftfmt.MaxArchiveBytes + 1, http.StatusRequestEntityTooLarge,
			`{"detail":"Swift source archive exceeds limit","status":413,"title":"Request Entity Too Large"}`,
		},
	} {
		body, contentType := zeroRelease(t, tc.size)
		wantAnswer(t, "archive of "+strconv.FormatInt(tc.size, 10)+" bytes",
			f.do(http.MethodPut, base+"acme/Widget/1.0.0", body, contentType, "admin"),
			tc.status, problemHeaders("", ""), tc.body)
	}
}

// TestAccSwiftPMPrintsProblemDetails: SwiftPM, configured as the client
// recipe configures it, prints a refused publish's detail, which it reads
// only from an error that carries Content-Version 1 (#1278): the
// middleware's 401 for bad credentials, the handler's 403 for a caller
// without write permission and its 409 for a version already published
// with other content.
// Without the header it printed "missing or invalid authentication
// credentials", "forbidden" and "client error 409: <body>".
func TestAccSwiftPMPrintsProblemDetails(t *testing.T) {
	if !swiftClientEnabled(t) {
		return
	}
	w := swiftWorld(t)
	srv := httptest.NewServer(w.H)
	defer srv.Close()
	root := t.TempDir()
	pkg := filepath.Join(root, "Widget")
	if err := os.MkdirAll(filepath.Join(pkg, "Sources", "Widget"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"Package.swift":               manifest,
		"Sources/Widget/Widget.swift": "public struct Widget { public init() {} }\n",
	} {
		path := filepath.Join(pkg, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "client-config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	// As in the recipe, an explicit basic authentication type for the host,
	// without which SwiftPM sends the netrc entry to no registry.
	if err := os.WriteFile(filepath.Join(config, "registries.json"),
		[]byte(`{"version":1,"registries":{},"authentication":{"127.0.0.1":{"type":"basic"}}}`),
		0o600); err != nil {
		t.Fatal(err)
	}
	publish := func(user, password string) (string, error) {
		t.Helper()
		netrc := filepath.Join(root, user+"-"+password+".netrc")
		if err := os.WriteFile(netrc,
			[]byte("machine 127.0.0.1 login "+user+" password "+password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := swiftCommand(t, root,
			"package-registry", "--package-path", swiftPath(root, pkg),
			"--netrc", "--netrc-file", swiftPath(root, netrc),
			"--config-path", swiftPath(root, config),
			"--security-path", swiftPath(root, filepath.Join(root, "security")),
			"publish", "acme.Widget", "1.0.0",
			"--url", srv.URL+"/artifactory/api/swift/swift-local", "--allow-insecure-http",
		).CombinedOutput()
		return string(out), err
	}
	// The bad password is reader's, not admin's: a failed login blocks the
	// user's next one for a moment, and admin publishes next.
	for _, c := range []struct{ user, password, printed string }{
		{"reader", "wrong", "server error 401: Bad credentials"},
		{"deployer", "pw", "server error 403: Forbidden"},
	} {
		out, err := publish(c.user, c.password)
		if err == nil || !strings.Contains(out, c.printed) {
			t.Errorf("SwiftPM publish as %s: %v, want %q in:\n%s", c.user, err, c.printed, out)
		}
	}
	if out, err := publish("admin", "pw"); err != nil {
		t.Fatalf("SwiftPM publish: %v\n%s", err, out)
	}
	// Changed sources, so the archive differs whatever zip's timestamps give
	// and the refusal is the format's, not the identical upload's.
	source := filepath.Join(pkg, "Sources", "Widget", "Widget.swift")
	if err := os.WriteFile(source, []byte("public struct Widget {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const conflict = "server error 409: Swift release 1.0.0 already exists with different content"
	if out, err := publish("admin", "pw"); err == nil || !strings.Contains(out, conflict) {
		t.Errorf("SwiftPM second publish: %v, want %q in:\n%s", err, conflict, out)
	}
}

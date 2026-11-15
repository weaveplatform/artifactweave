package artifacts_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

func TestAccSmartDiagnosticAuthorizationAndExactPath(t *testing.T) {
	calls := 0
	w := newItemWorld(t, true, func(c *Config) {
		c.RemoteStatus = func(ctx context.Context, key, path string) (smart.SmartStatus, error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Error("unbounded diagnostics")
			}
			if key == "missing" {
				return smart.SmartStatus{}, kernel.ErrNotFound
			}
			return smart.SmartStatus{
				Repository: key,
				Path:       path,
				Representations: []smart.SmartRepresentationStatus{
					{
						Downloads: 9007199254740993,
						Claimed:   7,
						Uncertain: 2,
						Work: []smart.SmartWorkStatus{
							{Operation: "statistics", State: work.WorkParked, Attempts: 1},
						},
					},
				},
			}, nil
		}
	})
	base := "/artifactory/api/artifactweave/remote-status/up"
	for _, tc := range []struct {
		user, path string
		status     int
	}{
		{"", base + "?path=file.bin", http.StatusUnauthorized},
		{"reader", base + "?path=file.bin", http.StatusForbidden},
		{"admin", base, http.StatusBadRequest},
		{"admin", base + "?path=../secret", http.StatusBadRequest},
		{"admin", base + "?path=*.bin", http.StatusBadRequest},
		{"admin", base + "?path=file.bin&path=other", http.StatusBadRequest},
		{"admin", base + "?path=file.bin&extra=1", http.StatusBadRequest},
		{"admin", base + "-cache?path=file.bin", http.StatusBadRequest},
	} {
		wantStatus(t, w.do(http.MethodGet, tc.path, tc.user, ""), tc.status)
	}
	if calls != 0 {
		t.Fatal("denied or invalid diagnostic called backend", calls)
	}
	rec := w.do(http.MethodGet, base+"?path=file.bin", "admin", "")
	wantStatus(t, rec, http.StatusOK)
	if rec.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(rec.Body.String(), `"downloads":9007199254740993`) ||
		!strings.Contains(rec.Body.String(), `"uncertain":2`) {
		t.Fatal(rec.Body.String())
	}
	wantStatus(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/artifactweave/remote-status/missing?path=file.bin",
			"admin",
			"",
		),
		http.StatusNotFound,
	)
}

// An anonymous caller reads a shared remote's configuration while anonymous
// access is on, and gets what a reader gets: 7.161.26's
// RestAuthenticationFilter gives an unauthenticated request ROLE_USER while
// anonymous access is on, which RepositoriesResource's class-level
// @RolesAllowed admits, and getRemoteDescriptor checks nothing more. A
// remote that does not share its configuration still answers 404. While
// anonymous access is off, AccessFilter's entry point answers a request
// without credentials 401 before the REST filter runs (#1509).
func TestAccSharedConfigurationAnonymousAccess(t *testing.T) {
	w := newRepoWorld(t)
	anonymous := func(on bool) {
		t.Helper()
		err := w.db.Settings().Put(t.Context(), settings.SettingAnonymousAccess, on)
		if err != nil {
			t.Fatal(err)
		}
	}
	anonymous(true)
	w.create(
		repo.Config{
			Key:                "shared",
			Class:              repo.Remote,
			URL:                "https://source.example/repository",
			ShareConfiguration: new(true),
			Username:           "private",
			Password:           "credential",
			CustomHTTPHeaders: []repo.CustomHTTPHeader{
				{Name: "X-Private", Value: "header-secret", Sensitive: true},
			},
		},
		repo.Config{
			Key: "unshared", Class: repo.Remote, URL: "https://source.example/repository",
			ShareConfiguration: new(false),
		},
	)
	path := "/artifactory/api/repositories/shared/configuration"
	reader := w.do(http.MethodGet, path, "reader", "")
	wantStatus(t, reader, http.StatusOK)
	for _, accept := range []string{"", mediaShared} {
		rec := w.do(http.MethodGet, path, "", "", "Accept", accept)
		wantStatus(t, rec, http.StatusOK)
		media := "application/json"
		if accept != "" {
			media = accept
		}
		if rec.Header().Get("Content-Type") != media || rec.Body.String() != reader.Body.String() {
			t.Errorf("anonymous %q: %s\n%s\nreader:\n%s", accept,
				rec.Header().Get("Content-Type"), rec.Body, reader.Body)
		}
		for _, secret := range []string{"private", "credential", "header-secret"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatal("shared configuration leaked protected field", secret)
			}
		}
	}
	for _, key := range []string{"unshared", "missing"} {
		rec := w.do(http.MethodGet, "/artifactory/api/repositories/"+key+"/configuration", "", "")
		if rec.Code != http.StatusNotFound ||
			rec.Body.String() != `{"errors":[{"message":"Not Found","status":404}]}`+"\n" {
			t.Errorf("anonymous %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	anonymous(false)
	rec := w.do(http.MethodGet, path, "", "")
	if rec.Code != http.StatusUnauthorized ||
		rec.Header().Get("WWW-Authenticate") != `Basic realm="Artifactory Realm"` ||
		rec.Body.String() != `{"errors":[{"message":"Authentication is required","status":401}]}`+"\n" {
		t.Errorf("anonymous access off: %d %q %s", rec.Code,
			rec.Header().Get("WWW-Authenticate"), rec.Body)
	}
}

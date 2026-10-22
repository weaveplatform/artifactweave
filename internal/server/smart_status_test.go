package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestSmartDiagnosticAuthorizationAndExactPath(t *testing.T) {
	calls := 0
	w := newItemWorld(t, true, func(c *Config) {
		c.RemoteStatus = func(ctx context.Context, key, path string) (remote.SmartStatus, error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Error("unbounded diagnostics")
			}
			if key == "missing" {
				return remote.SmartStatus{}, store.ErrNotFound
			}
			return remote.SmartStatus{
				Repository: key,
				Path:       path,
				Representations: []remote.SmartRepresentationStatus{
					{
						Downloads: 9007199254740993,
						Claimed:   7,
						Uncertain: 2,
						Work: []remote.SmartWorkStatus{
							{Operation: "statistics", State: types.WorkParked, Attempts: 1},
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

func TestSharedConfigurationRequiresAuthenticatedUser(t *testing.T) {
	w := newRepoWorld(t)
	if err := w.db.Settings().Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	w.create(
		repo.Config{
			Key:                "shared",
			Class:              repo.Remote,
			URL:                "https://source.example/repository",
			ShareConfiguration: new(true),
			Username:           "private",
			Password:           "credential",
			CustomHTTPHeaders: []repo.CustomHTTPHeader{
				{Name: "X-Private", Value: "header-secret"},
			},
		},
	)
	path := "/artifactory/api/repositories/shared/configuration"
	wantStatus(t, w.do(http.MethodGet, path, "", ""), http.StatusUnauthorized)
	rec := w.do(http.MethodGet, path, "reader", "")
	wantStatus(t, rec, http.StatusOK)
	for _, secret := range []string{"private", "credential", "header-secret", "customHttpHeaders"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("shared configuration leaked protected field", secret)
		}
	}
}

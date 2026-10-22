package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestSmartDiscoveryRejectsProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, media string
		status                  int
	}{
		{"null", "/api/system/version", "null", "", 200},
		{"html", "/api/system/version", "<html>login</html>", "text/html", 200},
		{"malformed", "/api/system/version", "{", "", 200},
		{"oversized", "/api/system/version", strings.Repeat("x", 1<<20+1), "", 200},
		{"version", "/api/system/version", `{"version":"unknown"}`, "", 200},
		{"old", "/api/system/version", `{"version":"3.9.0"}`, "", 200},
		{"missing", "/api/system/version", "", "", 404},
		{"throttled", "/api/system/version", "", "", 429},
		{"unsolicited304", "/api/system/version", "", "", 304},
		{"virtual", "/api/repositories/source", `{"rclass":"virtual"}`, "", 200},
		{"unknown", "/api/repositories/source", `{"rclass":"invalid"}`, "", 200},
		{"denied", "/api/repositories/source", "", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/artifactory"+tc.path {
					return false
				}
				if tc.media != "" {
					rw.Header().Set("Content-Type", tc.media)
				}
				rw.WriteHeader(tc.status)
				_, _ = io.WriteString(rw, tc.body)
				return true
			}
			if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
				t.Fatal("invalid discovery admitted")
			}
			if _, err := w.db.Items().
				Get(w.ctx, w.c.Key, true, "file.bin"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("discovery failure published", err)
			}
		})
	}
}

func TestSmartSharedDiscoveryAndNoStore(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/artifactory/api/repositories/source":
			rw.WriteHeader(403)
			return true
		case "/artifactory/api/repositories/source/configuration":
			rw.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(rw, `{"rclass":"remote"}`)
			return true
		}
		return false
	}
	w.smartRead()
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || state.Smart != nil {
		t.Fatal("private discovery was persisted", state, err)
	}
	w.smartRead()
	if smartTTL(repo.Config{}) != 2*time.Hour {
		t.Fatal("metadata default changed")
	}
}

func TestSmartPropertyDocumentAdmission(t *testing.T) {
	for _, body := range []string{`{}`, `{"properties":null}`, `{"properties":{"bad;key":["x"]}}`, `{"properties":{"empty":[]}}`, `{"properties":{"x":"not-an-array"}}`} {
		t.Run(body, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !r.URL.Query().Has("properties") {
					return false
				}
				_, _ = io.WriteString(rw, body)
				return true
			}
			if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
				t.Fatal("malformed property snapshot admitted")
			}
		})
	}
	for _, fault := range []string{"no-store", "empty-properties", "wrong-item", "missing"} {
		t.Run(fault, func(t *testing.T) {
			w := smartFixture(t)
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !strings.HasPrefix(r.URL.Path, "/artifactory/api/storage/source/") {
					return false
				}
				if r.URL.Query().Has("properties") {
					if fault == "no-store" {
						rw.Header().Set("Cache-Control", "no-store")
						return false
					}
					rw.WriteHeader(404)
					return true
				}
				if fault == "wrong-item" {
					_, _ = io.WriteString(rw, `{"repo":"other","path":"/file.bin"}`)
					return true
				}
				if fault == "missing" {
					rw.WriteHeader(404)
					return true
				}
				return false
			}
			result, err := w.service.Read(w.ctx, w.smartRequest())
			if fault == "empty-properties" {
				if err != nil || result.Item == nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid item metadata admitted")
			}
		})
	}
}

func TestSmartMetadataTargetAndRedirectFence(t *testing.T) {
	for _, redirect := range []string{"", "/artifactory/api/storage/other/private", "/artifactory/api/storage/source/file.bin/../private", "/artifactory/api/storage/source/private"} {
		t.Run(redirect, func(t *testing.T) {
			w := smartFixture(t)
			peer, err := ParseSmartPeer(w.c)
			if err != nil {
				t.Fatal(err)
			}
			target, err := peer.StorageURL("file.bin", url.Values{"list": {""}})
			if err != nil {
				t.Fatal(err)
			}
			q := w.smartRequest()
			q.Resource.Kind = source.Metadata
			q.Resource.RequireSmart = true
			q.Resource.Variant = "smart-listing-v1"
			q.Resource.ExternalURL = target
			q.Resource.CheckURL = func(_ context.Context, u *url.URL) error {
				if u.Path == "/artifactory/api/storage/source/private" {
					return source.ErrDenied
				}
				return nil
			}
			q.Resource.ExpectedContentType = "application/json"
			q.Resource.ValidateMetadata = func(context.Context, io.ReadSeeker, int64, source.MetadataResponse) error { return nil }
			if redirect != "" {
				w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
					if r.URL.Query().Has("list") {
						rw.Header().Set("Location", redirect)
						rw.WriteHeader(302)
						return true
					}
					return false
				}
			}
			result, err := w.service.Read(w.ctx, q)
			if redirect != "" {
				if err == nil {
					t.Fatal("metadata redirect escaped discovered repository")
				}
				return
			}
			if err != nil || result.Stream == nil {
				t.Fatal(err)
			}
			_ = result.Stream.Body.Close()
			for _, bad := range []string{peer.BaseURL + "/api/storage/other/file", peer.BaseURL + "/api/storage/sourcex/file", "https://other.example/api/storage/source/file", peer.BaseURL + "/api/storage/source/a%252fb", peer.BaseURL + "/api/storage/source/a//b"} {
				q.Resource.ExternalURL = bad
				if _, err = w.service.Read(w.ctx, q); err == nil {
					t.Fatal("metadata authority accepted", bad)
				}
			}
		})
	}
	if smartMetadataTarget(SmartPeer{}, "bad") {
		t.Fatal("invalid peer accepted")
	}
}

func TestSmartLoopBoundAndCapabilityLimits(t *testing.T) {
	for _, trace := range [][]string{{"node"}, {""}, {strings.Repeat("x", 257)}, {"control\n"}, {"1", "2", "3", "4", "5", "6", "7", "8"}} {
		ctx := context.WithValue(t.Context(), smartTraceKey{}, trace)
		if _, err := smartEnter(ctx, "node"); err == nil {
			t.Fatal("recursive or invalid chain accepted", trace)
		}
	}
	called := false
	h := SmartTrace(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		trace, _ := r.Context().Value(smartTraceKey{}).([]string)
		if len(trace) != 8 {
			t.Error("unbounded incoming trace", len(trace))
		}
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(SmartOriginHeader, strings.Repeat("node,", 10000))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("middleware failed")
	}
	for _, tc := range []struct {
		version string
		kind    repo.PackageType
		class   repo.Class
		ok      bool
	}{
		{"4.1.0", repo.Generic, repo.Local, true}, {"4.2.0", repo.Generic, repo.Local, true}, {"4.3.2", repo.Maven, repo.Local, true}, {"4.3.3", repo.Maven, repo.Local, true}, {"7.133.8", repo.Go, repo.Virtual, true}, {"7.133.8", repo.Go, repo.Local, false}, {"7.133.8", repo.Generic, repo.Virtual, false}, {"7.x.0", repo.Generic, repo.Local, false}, {"7.1", repo.Generic, repo.Local, false}, {"-1.2.0", repo.Generic, repo.Local, false},
	} {
		c := repo.Config{PackageType: tc.kind}
		err := smartCapabilities(c, SmartPeer{Version: tc.version, Class: tc.class})
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
	c := repo.Config{
		PackageType: repo.Maven,
		ContentSynchronisation: &repo.ContentSynchronisation{
			Enabled:    true,
			Statistics: &repo.EnabledFlag{Enabled: true},
			Source:     &repo.ContentSyncSource{OriginAbsenceDetection: true},
		},
	}
	for _, version := range []string{"4.1.0", "4.3.2"} {
		if err := smartCapabilities(c, SmartPeer{Version: version, Class: repo.Local}); err == nil {
			t.Fatal("unsupported capability", version)
		}
	}
}

func TestSmartRegistryGrantAndPropertyPath(t *testing.T) {
	for _, realm := range []string{"", "remote", "other"} {
		name := "image"
		if realm == "" {
			name = "remote/image"
		}
		access := &types.RegistryAccess{
			Realm: realm,
			Grants: []types.RegistryGrant{
				{Type: "repository", Name: name, Actions: []string{"pull"}},
			},
		}
		if smartRegistryGrant(access, "remote", "image/manifests/tag") != (realm != "other") {
			t.Fatal("realm grant", realm)
		}
		if smartRegistryGrant(access, "remote", "bad") ||
			smartRegistryGrant(access, "remote", "private/manifests/tag") {
			t.Fatal("scope widened")
		}
	}
	for _, ref := range []string{"tag", "sha256:" + strings.Repeat("a", 64)} {
		resource, err := RegistryResource("image", "manifests", ref)
		if err != nil {
			t.Fatal(err)
		}
		q := source.Request{Repo: repo.Config{PackageType: repo.OCI}, Resource: resource}
		if smartPropertyPath(q) != (ref == "tag") || smartStoragePath(q) != q.Resource.Path {
			t.Fatal("digest pull synchronized tag properties")
		}
	}
}

package ansible_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/format/ansible"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func remoteVersionJSON(version, download string, data []byte) []byte {
	sum := sha256.Sum256(data)
	raw, _ := json.Marshal(map[string]any{
		"version": version, "namespace": map[string]string{"name": "weave"},
		"collection": map[string]string{"name": "sample"}, "download_url": download,
		"artifact": map[string]string{"sha256": hex.EncodeToString(sum[:])},
		"metadata": map[string]any{"dependencies": map[string]string{}},
	})
	return raw
}

func TestAccGalaxyRechecksVersionPermissionBeforeMetadataDelivery(t *testing.T) {
	for _, endpoint := range []string{
		"/api/v3/collections/weave/sample/",
		remoteList,
		remoteList + "1.2.3/",
	} {
		t.Run(endpoint, func(t *testing.T) {
			f, up, _, base := remoteFixture(t)
			up.Set(
				remoteList,
				fakeupstream.Resource{
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   []byte(`{"data":[{"version":"1.2.3"},{"version":"1.2.4"}]}`),
				},
			)
			up.Set(
				remoteList+"1.2.4/",
				fakeupstream.Resource{
					Header: http.Header{"Content-Type": {"application/json"}},
					Body:   remoteVersionJSON("1.2.4", up.URL+"/file", f.data),
				},
			)
			var revoked atomic.Bool
			f.source.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				// The collection folder remains readable while a later version read
				// revokes the earlier version's exact artifact permission.
				if q.Resource.Kind == source.ResourceKindMetadata &&
					strings.HasSuffix(q.Resource.Path, "/1.2.4") {
					revoked.Store(true)
				}
				if revoked.Load() && q.Resource.Kind == source.ResourceKindArtifact &&
					q.Resource.Path == ansiblefmt.Path("weave", "sample", "1.2.3") {
					return source.ErrDenied
				}
				return nil
			}
			w := f.do("GET", base+endpoint, nil, "", "")
			requireStatus(t, w, http.StatusForbidden)
			if !revoked.Load() || strings.Contains(w.Body.String(), "1.2.3") {
				t.Fatalf("revoked version was disclosed: %s", w.Body)
			}
		})
	}
}

func TestAccGalaxyRetainsEarlierMemberSourceFence(t *testing.T) {
	for _, change := range []string{"source", "credential", "empty source", "empty credential"} {
		t.Run(change, func(t *testing.T) {
			f, up, first, _ := remoteFixture(t)
			endpoint := remoteList
			if strings.HasPrefix(change, "empty ") {
				up.Set(
					remoteList,
					fakeupstream.Resource{
						Header: http.Header{"Content-Type": {"application/json"}},
						Body:   []byte(`{"data":[]}`),
					},
				)
				if change == "empty source" {
					endpoint = "/api/v3/collections/weave/sample/"
				} else {
					endpoint = remoteList + "1.2.3/"
				}
			}
			router := chi.NewRouter()
			router.Get(remoteList, func(w http.ResponseWriter, r *http.Request) {
				changed := first
				if strings.Contains(change, "source") {
					changed.URL += "/replacement"
				} else {
					changed.Password = "rotated-upstream-credential"
				}
				if err := f.db.Repos().Update(r.Context(), changed); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[],"links":{"next":null}}`))
			})
			later := httptest.NewServer(router)
			t.Cleanup(later.Close)
			second := *repo.Defaults(&repo.Config{Key: "second", Class: repo.Remote, PackageType: repo.Ansible, URL: later.URL})
			virtual := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible, Repositories: []string{first.Key, second.Key}})
			for _, c := range []repo.Config{second, virtual} {
				if err := f.db.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			w := f.do("GET", "/artifactory/api/ansible/virtual"+endpoint, nil, "", "")
			requireStatus(t, w, http.StatusServiceUnavailable)
			if strings.Contains(w.Body.String(), "1.2.3") {
				t.Fatalf("stale member was disclosed: %s", w.Body)
			}
		})
	}
}

func TestAccGalaxyCDNArchiveStripsCredentialsAndHonorsPatterns(t *testing.T) {
	var data []byte
	var requests atomic.Int32
	router := chi.NewRouter()
	router.Get("/collection.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Upstream-Secret") != "" {
			t.Error("source credentials reached CDN")
		}
		_, _ = w.Write(data)
	})
	cdn := httptest.NewServer(router)
	t.Cleanup(cdn.Close)
	f, up, c, base := remoteFixture(t)
	data = f.data
	c.Username = "upstream-reader"
	c.Password = "upstream-secret"
	c.CustomHTTPHeaders = []repo.CustomHTTPHeader{{Name: "X-Upstream-Secret", Value: "private"}}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: []string{cdn.URL},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.source.HTTP.(*remote.Policy).Options = resolver.Options
	up.Set(
		remoteList+"1.2.3/",
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   remoteVersionJSON("1.2.3", cdn.URL+"/collection.tar.gz", data),
		},
	)
	endpoint := base + "/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz"
	w := f.do("GET", endpoint, nil, "", "")
	requireStatus(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) || requests.Load() != 1 {
		t.Fatal("CDN archive was not delivered")
	}
	authenticated := false
	for _, request := range up.Requests() {
		if request.HeaderMatches(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password)),
		) &&
			request.HeaderMatches("X-Upstream-Secret", "private") {
			authenticated = true
		}
	}
	if !authenticated {
		t.Fatal("configured source credentials were not sent to the source")
	}
	c.ExcludesPattern = new("collections/weave/sample/1.2.3/**")
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	// The shared CheckRead boundary normalizes a pattern rejection to an
	// unavailable result. It must not disclose metadata or contact the CDN.
	requireStatus(t, f.do("GET", endpoint, nil, "", ""), http.StatusBadGateway)
	if requests.Load() != 1 {
		t.Fatal("excluded artifact contacted CDN")
	}
}

func TestAccGalaxyFiltersUnreadableRemoteVersions(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	up.Set(
		remoteList,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"data":[{"version":"1.2.3"},{"version":"2.0.0"}]}`),
		},
	)
	permission := types.Permission{
		Name: "selected-version",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}},
				Targets: map[string]types.Target{
					c.Key: {Include: []string{"collections/weave/sample/1.2.3/**"}},
				},
			},
		},
	}
	if err := f.db.Permissions().Create(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", base+remoteList, nil, "", "reader")
	requireStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "2.0.0") || !strings.Contains(w.Body.String(), "1.2.3") ||
		up.Count("GET", remoteList+"2.0.0/") != 0 {
		t.Fatalf("unreadable remote version was fetched or disclosed: %s", w.Body)
	}
}

func TestAccGalaxyRetainsCatalogFencesDuringArchiveDownload(t *testing.T) {
	f, up, first, _ := remoteFixture(t)
	second := *repo.Defaults(&repo.Config{Key: "second", Class: repo.Remote, PackageType: repo.Ansible, URL: up.URL})
	virtual := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.Ansible, Repositories: []string{first.Key, second.Key}})
	for _, c := range []repo.Config{second, virtual} {
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	router := chi.NewRouter()
	router.Get("/archive.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		second.Password = "changed-during-first-member-download"
		if err := f.db.Repos().Update(r.Context(), second); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(f.data)
	})
	cdn := httptest.NewServer(router)
	t.Cleanup(cdn.Close)
	resolver, err := remote.NewResolver(
		f.db,
		nil,
		remote.ResolverOptions{
			RevisionKey:           []byte(strings.Repeat("k", 32)),
			AllowedPrivateOrigins: []string{cdn.URL},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.source.HTTP.(*remote.Policy).Options = resolver.Options
	up.Set(
		remoteList+"1.2.3/",
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   remoteVersionJSON("1.2.3", cdn.URL+"/archive.tar.gz", f.data),
		},
	)
	w := f.do(
		"GET",
		"/artifactory/api/ansible/virtual/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz",
		nil,
		"",
		"",
	)
	requireStatus(t, w, 503)
	if bytes.Equal(w.Body.Bytes(), f.data) {
		t.Fatal("archive delivered despite stale catalog member")
	}
}

func TestAccGalaxyRejectsArchiveIdentityEvenWhenAdvertisedHashMatches(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	up.Set(
		remoteList,
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   []byte(`{"data":[{"version":"1.2.4"}]}`),
		},
	)
	up.Set(
		remoteList+"1.2.4/",
		fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   remoteVersionJSON("1.2.4", up.URL+"/files/weave-sample-1.2.3.tar.gz", f.data),
		},
	)
	w := f.do(
		"GET",
		base+"/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.4.tar.gz",
		nil,
		"",
		"",
	)
	requireStatus(t, w, 502)
	if _, err := f.db.Items().
		Get(t.Context(), c.Key, true, ansiblefmt.Path("weave", "sample", "1.2.4")); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("invalid archive identity cached: %v", err)
	}
}

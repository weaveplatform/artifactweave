package maven_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type retainedSQLFault struct {
	remote.Store
	db *appsql.Store
}

func (s retainedSQLFault) RemoteCache() port.RemoteCacheStore {
	return retainedSQLCache{RemoteCacheStore: s.Store.RemoteCache(), db: s.db}
}

type retainedSQLCache struct {
	port.RemoteCacheStore
	db *appsql.Store
}

func (c retainedSQLCache) Put(
	ctx context.Context, _ model.RemoteCacheToken, _ model.RemoteCacheEntry,
) (model.RemoteCacheEntry, error) {
	// A real statement fails inside the publication transaction, through the
	// same driver/error wrapper as the real remote_cache UPDATE.
	_, err := c.db.Exec(ctx, "unexpire retained cache", "UPDATE remote_cache_held SET doc = doc")
	return model.RemoteCacheEntry{}, err //nolint:wrapcheck // retain the real driver's cause
}

func TestAccMavenExpiredRemoteClockStoreFaultIsInternal(t *testing.T) {
	for _, readAlone := range []bool{false, true} {
		for _, phase := range []string{"read", "put"} {
			t.Run(fmt.Sprintf("read-only=%v/%s", readAlone, phase), func(t *testing.T) {
				w := newMavenRemoteWorld(t)
				w.c.RetrievalCachePeriodSecs = new(0)
				w.update()
				mavenSource(w, expiredSnapshot, sourceJAR(t, "old"), "application/java-archive")
				remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+expiredSnapshot, "reader", ""), 200)
				entries, _, err := w.rt.Store.RemoteCache().Exact(t.Context(), "remote", expiredSnapshot, kernel.Page{Limit: 10})
				if err != nil || len(entries) != 1 {
					t.Fatal(entries, err)
				}
				before := entries[0]
				if readAlone {
					expiredReadAlone(t, w)
				}
				var logs bytes.Buffer
				w.h = harness.Router(harness.Config{
					Store: w.rt.Store, Blobs: w.rt.Blobs, Auth: w.rt.Auth, Tokens: w.rt.Tokens, Keys: w.rt.Keys,
					RemoteSources: w.rt.Artifacts.RemoteSources(), RemoteHTTP: w.rt.Artifacts.RemoteHTTP(),
					Logger: slog.New(slog.NewTextHandler(&logs, nil)),
				})
				p := w.rt.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
				if phase == "read" {
					p.ProjectResponse = func(_ source.Request, res *http.Response) error {
						if res.StatusCode == http.StatusNotFound {
							renameTable(t, w, "remote_cache", "remote_cache_held")
						}
						return nil
					}
				} else {
					p.Store = retainedSQLFault{Store: p.Store, db: w.rt.Store}
				}
				w.up.Set("/"+expiredSnapshot, fakeupstream.Resource{Status: http.StatusNotFound})
				got := w.request(http.MethodGet, "/artifactory/remote/"+expiredSnapshot, "reader", "")
				if phase == "read" {
					renameTable(t, w, "remote_cache_held", "remote_cache")
				}
				if got.Code != http.StatusInternalServerError || got.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
					t.Fatalf("own clock/cache fault: %d %q", got.Code, got.Body)
				}
				if n := strings.Count(logs.String(), "level=ERROR"); n != 1 || !strings.Contains(logs.String(), "remote_cache") {
					t.Fatalf("original SQL fault must be logged once: %s", logs.String())
				}
				after, err := w.rt.Store.RemoteCache().Read(t.Context(), before.Token.Key)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("failed clock transaction changed content or negative state", before, after, err)
				}
			})
		}
	}
}

type retainedStatHook struct {
	blob.Store
	sha string
	fn  func()
}

func (s retainedStatHook) Stat(ctx context.Context, sha string) (blob.Info, error) {
	info, err := s.Store.Stat(ctx, sha)
	if err == nil && sha == s.sha {
		s.fn()
	}
	return info, err //nolint:wrapcheck // preserve the blob store's fault
}

func TestAccMavenExpiredRemoteRechecksCurrentAuthority(t *testing.T) {
	for _, mutation := range []string{"read", "pattern", "source", "invalidate", "replacement"} {
		t.Run(mutation, func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RetrievalCachePeriodSecs = new(0)
			w.update()
			mavenSource(w, expiredSnapshot, sourceJAR(t, "old"), "application/java-archive")
			remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+expiredSnapshot, "reader", ""), 200)
			entries, _, err := w.rt.Store.RemoteCache().Exact(t.Context(), "remote", expiredSnapshot, kernel.Page{Limit: 10})
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
			before := entries[0]
			p := w.rt.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
			p.Blobs = retainedStatHook{Store: w.rt.Blobs, sha: before.Blob.Checksums.SHA256, fn: func() {
				switch mutation {
				case "read":
					err = w.rt.Store.Permissions().Delete(t.Context(), "remote-read")
				case "pattern":
					w.c.ExcludesPattern = new("**")
					err = w.rt.Store.Repos().Update(t.Context(), w.c)
				case "source":
					w.c.URL += "/changed"
					err = w.rt.Store.Repos().Update(t.Context(), w.c)
				case "invalidate":
					_, err = w.rt.Store.RemoteCache().Invalidate(t.Context(), "remote", expiredSnapshot, false)
				case "replacement":
					before, err = w.rt.Store.RemoteCache().Put(t.Context(), before.Token, before)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			w.up.Set("/"+expiredSnapshot, fakeupstream.Resource{Status: http.StatusNotFound})
			got := w.request(http.MethodGet, "/artifactory/remote/"+expiredSnapshot, "reader", "")
			want := http.StatusServiceUnavailable
			if mutation == "read" {
				want = http.StatusForbidden
			}
			if got.Code != want || bytes.Contains(got.Body.Bytes(), []byte("retained bytes")) {
				t.Fatalf("current %s must refuse disclosure: %d %s", mutation, got.Code, got.Body.String())
			}
			if mutation == "read" || mutation == "pattern" || mutation == "replacement" {
				after, readErr := w.rt.Store.RemoteCache().Read(t.Context(), before.Token.Key)
				if readErr != nil || !after.ValidatedAt.Equal(before.ValidatedAt) || !after.NegativeUntil.IsZero() {
					t.Fatal("refused retention advanced the clock or wrote a negative", after, readErr)
				}
			}
		})
	}
}

package maven_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	mavenfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const expiredSnapshot = "org/acme/retained/1.0-SNAPSHOT/retained-1.0-SNAPSHOT.jar"

func expiredFetchCount(w *remoteWorld, path string) func() int64 {
	p := w.rt.Artifacts.RemoteSources().HTTP.(*mavenfmt.MavenBackend).Policy
	previous := p.Fetch
	var calls atomic.Int64
	p.Fetch = func(ctx context.Context, q source.Request, permit source.Permit, opts remote.Options, request remote.Request) (*http.Response, error) {
		if q.Resource.Path == path {
			calls.Add(1)
		}
		if previous != nil {
			return previous(ctx, q, permit, opts, request)
		}
		conn, err := p.Pool.Connection(q.Repo, opts)
		if err != nil {
			return nil, fmt.Errorf("observe upstream connection: %w", err)
		}
		return conn.Fetch(ctx, request) //nolint:wrapcheck // observe the real protocol exchange without changing its failure
	}
	return calls.Load
}

// originExchanges describes each request the origin recorded for path, in
// arrival order, by method, connection and conditional validators.
func originExchanges(w *remoteWorld, path string) []string {
	var out []string
	for _, r := range w.up.Requests() {
		if r.Path == "/"+path {
			out = append(out, fmt.Sprintf("%s from %s If-None-Match=%q If-Modified-Since=%q",
				r.Method, r.RemoteAddr, r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")))
		}
	}
	return out
}

func expiredReadAlone(t *testing.T, w *remoteWorld) {
	t.Helper()
	if err := w.rt.Store.Permissions().Update(t.Context(), accesstest.Permission{
		Name: "remote-read",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"remote": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccMavenExpiredRemoteRetainsFiles(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		for _, readAlone := range []bool{false, true} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, answer := range []string{"404", "500", "timeout"} {
					t.Run(fmt.Sprintf("metadata=%v/read-only=%v/%s/%s", metadata, readAlone, method, answer), func(t *testing.T) {
						w := newMavenRemoteWorld(t)
						w.c.RetrievalCachePeriodSecs = new(0)
						w.c.AssumedOfflinePeriodSecs = new(0)
						if answer == "timeout" {
							// Only the timeout answer gets a short socket timeout, set
							// before priming because the cache key carries the settings
							// revision. A loaded race build can take over 100 ms to
							// relay a prompt 404 or 500, which would then be tested as
							// a timeout the subtest did not ask for.
							w.c.SocketTimeoutMillis = new(100)
						}
						w.update()
						path, data, contentType := expiredSnapshot, sourceJAR(t, "retained bytes"), "application/java-archive"
						if metadata {
							path = "org/acme/retained/maven-metadata.xml"
							data = []byte(
								`<metadata><groupId>org.acme</groupId><artifactId>retained</artifactId><versioning><versions><version>1.0</version></versions></versioning></metadata>`,
							)
							contentType = "application/xml"
						}
						fetches := expiredFetchCount(w, path)
						mavenSource(w, path, data, contentType)
						remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+path, "reader", ""), http.StatusOK)
						entries, _, err := w.rt.Store.RemoteCache().Exact(t.Context(), "remote", path, kernel.Page{Limit: 10})
						if err != nil || len(entries) != 1 {
							t.Fatal("primed representation", entries, err)
						}
						before := entries[0]
						if readAlone {
							expiredReadAlone(t, w)
						}
						switch answer {
						case "404":
							w.up.Set("/"+path, fakeupstream.Resource{Status: http.StatusNotFound})
						case "500":
							w.up.Set("/"+path, fakeupstream.Resource{Status: http.StatusInternalServerError})
						case "timeout":
							gate := new(fakeupstream.Barrier)
							defer gate.Release()
							w.up.Set("/"+path, fakeupstream.Resource{Fault: fakeupstream.Fault{BeforeHeaders: gate}})
						}
						requests := len(originExchanges(w, path))
						beforeFetch := fetches()
						got := w.request(method, "/artifactory/remote/"+path, "reader", "")
						remoteStatus(t, got, http.StatusOK)
						if method == http.MethodGet && !bytes.Equal(got.Body.Bytes(), data) {
							t.Fatal("retained response changed bytes")
						}
						if method == http.MethodHead && got.Body.Len() != 0 {
							t.Fatal("HEAD returned a body")
						}
						// One policy fetch is one origin exchange for every answer: the
						// remote client never leaves net/http a failed first read on a
						// reused connection to replay (#1866). Each record names its
						// connection, so a replay shows as a second one.
						if exchanges := originExchanges(w, path)[requests:]; fetches()-beforeFetch != 1 || len(exchanges) != 1 {
							t.Fatal("origin policy fetches and physical exchanges", fetches()-beforeFetch, exchanges)
						}
						after, err := w.rt.Store.RemoteCache().Read(t.Context(), before.Token.Key)
						if err != nil || !after.NegativeUntil.IsZero() {
							t.Fatal("retained file became a negative", after, err)
						}
						if answer == "404" {
							if !after.ValidatedAt.After(before.ValidatedAt) || after.Token.Revision != before.Token.Revision+1 {
								t.Fatal("origin miss did not unexpire the file", before, after)
							}
							before.ValidatedAt, before.Token = after.ValidatedAt, after.Token
						}
						// LastUsed is independent read accounting. Payload identity,
						// validators and original fetch time survive every fallback.
						before.LastUsed = after.LastUsed
						if !reflect.DeepEqual(before, after) {
							t.Fatal("retention changed content or its clock after a network failure", before, after)
						}
						requests = w.up.Count("", "/"+path)
						alias := w.request(method, "/artifactory/remote-cache/"+path, "reader", "")
						remoteStatus(t, alias, http.StatusOK)
						if w.up.Count("", "/"+path) != requests {
							t.Fatal("cache alias contacted origin")
						}
						if !metadata {
							for _, kind := range []repo.PackageType{repo.Maven, repo.Generic} {
								key := string(kind) + "-virtual"
								v := *repo.Defaults(&repo.Config{Key: key, Class: repo.Virtual, PackageType: kind, Repositories: []string{"remote"}})
								if err = w.rt.Store.Repos().Create(t.Context(), v); err != nil {
									t.Fatal(err)
								}
								remoteStatus(t, w.request(method, "/artifactory/"+key+"/"+path, "reader", ""), http.StatusOK)
								if method == http.MethodGet {
									remoteStatus(t, w.request(http.MethodHead, "/artifactory/"+key+"/"+path, "reader", ""), http.StatusOK)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestAccMavenFreshAndUniqueRemoteNeverRevalidates(t *testing.T) {
	for _, unique := range []bool{false, true} {
		t.Run(fmt.Sprintf("unique=%v", unique), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			path := expiredSnapshot
			w.c.RetrievalCachePeriodSecs = new(3600)
			if unique {
				w.c.RetrievalCachePeriodSecs = new(0)
				path = "org/acme/retained/1.0-SNAPSHOT/retained-1.0-20261021.120000-1.jar"
			}
			w.update()
			data := sourceJAR(t, "fresh")
			mavenSource(w, path, data, "application/java-archive")
			remoteStatus(t, w.request(http.MethodGet, "/artifactory/remote/"+path, "reader", ""), 200)
			w.up.Set("/"+path, fakeupstream.Resource{Status: http.StatusNotFound})
			requests := w.up.Count("", "/"+path)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				remoteStatus(t, w.request(method, "/artifactory/remote/"+path, "reader", ""), 200)
			}
			if w.up.Count("", "/"+path) != requests {
				t.Fatal("fresh/unique file was revalidated")
			}
		})
	}
}

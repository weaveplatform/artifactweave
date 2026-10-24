package nuget_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/reindexauth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func cacheReindexPackage(t *testing.T, w *world, path string, body []byte) types.Item {
	t.Helper()
	it, err := w.items.Deploy(
		w.ctx,
		item.Deploy{
			Target: item.Target{Repo: w.c, Cached: true, Path: path},
			Body:   bytes.NewReader(body),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestUnitRemoteReindexRejectsMalformedCacheAtomically(t *testing.T) {
	w := newWorld(t, nil)
	w.c = w.repo("remote", repo.Remote, nil)
	good := cacheReindexPackage(t, w, "Good.1.0.0.nupkg", pkg("Good", "1.0.0", ""))
	cacheReindexPackage(t, w, "Broken.1.0.0.nupkg", []byte("broken archive"))
	if err := w.st.Items().
		SetProperties(w.ctx, good.ID, types.Properties{nuget.PropID: {"needs repair"}}, w.now); err != nil {
		t.Fatal(err)
	}
	if err := w.items.Reindex(w.ctx, w.c); !errors.Is(err, nuget.ErrNotPackage) {
		t.Fatalf("malformed cache: %v", err)
	}
	props, err := w.st.Items().Properties(w.ctx, good.ID)
	if err != nil || props[nuget.PropID][0] != "needs repair" {
		t.Fatalf("partial publication: %v %v", props, err)
	}
	if len(w.index(w.c)["good"]) != 1 {
		t.Fatal("valid index lost after failure")
	}
}

// Both persistent backends retain the payload, failure and retry state across
// worker replacement. They publish the repaired index and properties atomically.
func TestUnitRemoteReindexStores(t *testing.T) {
	for _, backend := range formattest.Backends() {
		t.Run(backend.Name, func(t *testing.T) {
			w := newWorldOn(t, backend.Open(t), nil)
			w.c = w.repo("remote", repo.Remote, nil)
			it := cacheReindexPackage(
				t,
				w,
				"foo/1.0.0/Foo.1.0.0.nupkg",
				pkg("Foo", "1.0.0", "<title>Original</title>"),
			)
			cacheReindexPackage(t, w, "Foo.1.0.0.snupkg", symbols("Foo", "1.0.0"))
			expected := w.index(w.c)
			p, err := w.st.Packages().Find(w.ctx, w.c.Key, "foo")
			if err != nil {
				t.Fatal(err)
			}
			if err = w.st.Packages().Delete(w.ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			if err = w.st.Items().
				SetProperties(w.ctx, it.ID, types.Properties{nuget.PropID: {"wrong"}, "keep": {"mine"}}, w.now); err != nil {
				t.Fatal(err)
			}
			raw := cacheReindexRaw(t, w)
			queueRemoteReindex(t, w)
			abandoned, took, err := w.st.WorkQueue().
				LeaseKey(w.ctx, format.KindReindex, w.c.Key, store.WorkLease{Holder: "stopped-worker", Now: w.now, Until: w.now.Add(time.Minute)})
			if err != nil || !took || abandoned.State != types.WorkLeased {
				t.Fatal("lease before restart", abandoned, err)
			}
			w.now = w.now.Add(2 * time.Minute)
			// Fail after annotations would have been repaired. The transaction must
			// roll all of them back and keep a retryable item in the durable queue.
			faultyStore := &faulty{Store: w.st, f: &faults{pass: map[string]int{"PutVersion": 0}}}
			svc := &item.Service{
				Store: faultyStore,
				Blobs: w.items.Blobs,
				Now:   func() time.Time { return w.now },
			}
			q := restartedReindexWorker(t, w, svc)
			if err = q.EnsureFresh(w.ctx, format.KindReindex, w.c.Key); !errors.Is(err, errFault) {
				t.Fatal("injected failure", err)
			}
			pending, err := w.st.WorkQueue().Get(w.ctx, format.KindReindex, w.c.Key)
			if err != nil || pending.State != types.WorkPending || pending.Attempts != 2 {
				t.Fatalf("failure not durable: %+v %v", pending, err)
			}
			props, err := w.st.Items().Properties(w.ctx, it.ID)
			if err != nil || props[nuget.PropID][0] != "wrong" || len(w.index(w.c)) != 0 {
				t.Fatal("partial failed rebuild", props, err)
			}
			w.now = w.now.Add(time.Hour)
			q = restartedReindexWorker(
				t,
				w,
				&item.Service{
					Store: w.st,
					Blobs: w.items.Blobs,
					Now:   func() time.Time { return w.now },
				},
			)
			if err = q.EnsureFresh(w.ctx, format.KindReindex, w.c.Key); err != nil {
				t.Fatal(err)
			}
			if _, err = w.st.WorkQueue().
				Get(w.ctx, format.KindReindex, w.c.Key); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("worker did not complete", err)
			}
			if diff := cmp.Diff(expected, w.index(w.c)); diff != "" {
				t.Fatal(diff)
			}
			props, err = w.st.Items().Properties(w.ctx, it.ID)
			if err != nil || props[nuget.PropID][0] != "Foo" ||
				props[nuget.PropTitle][0] != "Original" ||
				props["keep"][0] != "mine" {
				t.Fatal("properties not repaired", props, err)
			}
			saved, err := w.st.RemoteCache().Read(w.ctx, raw.Token.Key)
			if err != nil || !reflect.DeepEqual(saved, raw) {
				t.Fatal("raw upstream metadata changed", saved, err)
			}
			// An empty cache completes and removes an orphan index row, without
			// manufacturing package rows from the upstream version list.
			empty := w.repo("empty", repo.Remote, nil)
			if _, err = w.st.Packages().
				Ensure(w.ctx, types.Package{RepoKey: empty.Key, Name: "orphan", Type: repo.NuGet, Created: w.now}); err != nil {
				t.Fatal(err)
			}
			w.c = empty
			queueRemoteReindex(t, w)
			if err = q.EnsureFresh(w.ctx, format.KindReindex, empty.Key); err != nil {
				t.Fatal(err)
			}
			if len(w.index(empty)) != 0 {
				t.Fatal("empty cache invented packages")
			}
		})
	}
}

func cacheReindexRaw(t *testing.T, w *world) types.RemoteCacheEntry {
	t.Helper()
	token, err := w.st.RemoteCache().
		Begin(w.ctx, types.RemoteCacheKey{RepoKey: w.c.Key, Path: ".upstream/versions", Variant: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"versions":["1.0.0","99.0.0"]}`
	result, err := w.items.Cache(
		w.ctx,
		item.CacheUpload{
			Repo:        w.c,
			Token:       token,
			Kind:        types.RemoteMetadata,
			Body:        strings.NewReader(body),
			Length:      int64(len(body)),
			ContentType: "application/json",
			RemoteURL:   w.c.URL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result.Entry
}

func queueRemoteReindex(t *testing.T, w *world) {
	t.Helper()
	source, err := w.st.RemoteCache().Source(w.ctx, w.c.Key)
	if err != nil {
		t.Fatal(err)
	}
	credential := reindexCredential(t, w)
	payload, err := json.Marshal(
		format.WorkRequest{By: "tester", Source: &source, Credential: credential},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.st.WorkQueue().
		Enqueue(w.ctx, format.KindReindex, w.c.Key, payload, w.now); err != nil {
		t.Fatal(err)
	}
}

func restartedReindexWorker(t *testing.T, w *world, svc *item.Service) *work.Queue {
	t.Helper()
	q, err := work.New(
		w.st,
		work.Options{
			Now:     func() time.Time { return w.now },
			Backoff: func(int) time.Duration { return time.Millisecond },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if svc.ReindexAuthorize == nil {
		svc.ReindexAuthorize = w.items.ReindexAuthorize
	}
	if err = svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestUnitRemoteReindexQueuedSourceFences(t *testing.T) {
	for _, mutation := range []string{"changed", "away and back", "recreated", "deleted", "unbound", "wrong payload"} {
		t.Run(mutation, func(t *testing.T) {
			w := newWorld(t, nil)
			w.c = w.repo("remote", repo.Remote, nil)
			cacheReindexPackage(t, w, "Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", ""))
			queueRemoteReindex(t, w)
			switch mutation {
			case "changed", "away and back":
				changed := w.c
				changed.URL = "https://other.example"
				if err := w.st.Repos().Update(w.ctx, changed); err != nil {
					t.Fatal(err)
				}
				if mutation == "away and back" {
					if err := w.st.Repos().Update(w.ctx, w.c); err != nil {
						t.Fatal(err)
					}
				}
			case "recreated", "deleted":
				if err := w.st.Repos().Delete(w.ctx, w.c.Key); err != nil {
					t.Fatal(err)
				}
				if mutation == "recreated" {
					w.c = w.repo("remote", repo.Remote, nil)
				}
			case "unbound", "wrong payload":
				var payload []byte
				if mutation == "wrong payload" {
					payload = []byte(`"invalid payload"`)
				}
				if err := w.st.WorkQueue().
					Enqueue(w.ctx, format.KindReindex, w.c.Key, payload, w.now); err != nil {
					t.Fatal(err)
				}
			}
			err := restartedReindexWorker(
				t,
				w,
				w.items,
			).EnsureFresh(w.ctx, format.KindReindex, w.c.Key)
			if mutation == "deleted" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, work.ErrPermanent) {
				t.Fatalf("obsolete work: %v", err)
			}
			pending, err := w.st.WorkQueue().Get(w.ctx, format.KindReindex, w.c.Key)
			if err != nil || pending.State != types.WorkParked {
				t.Fatal("obsolete work not parked", pending, err)
			}
			if mutation != "wrong payload" && mutation != "unbound" {
				queueRemoteReindex(t, w)
				if err := w.q.EnsureFresh(w.ctx, format.KindReindex, w.c.Key); err != nil {
					t.Fatal("fresh request", err)
				}
			}
		})
	}
}

// The byte reader is the concurrency barrier: mutations finish before the
// publication transaction starts. A retry must inspect the replacement bytes.
func TestUnitRemoteReindexConcurrentChanges(t *testing.T) {
	for _, mutation := range []string{"overwrite", "admit", "delete", "source", "properties"} {
		t.Run(mutation, func(t *testing.T) {
			w := newWorld(t, nil)
			w.c = w.repo("remote", repo.Remote, nil)
			it := cacheReindexPackage(
				t,
				w,
				"Foo.1.0.0.nupkg",
				pkg("Foo", "1.0.0", "<title>Old</title>"),
			)
			queueRemoteReindex(t, w)
			original := w.items.Blobs
			svc := *w.items
			svc.Blobs = &reindexHookBlobs{Store: original, hook: func() {
				switch mutation {
				case "overwrite", "admit":
					name := "Foo"
					if mutation == "admit" {
						name = "Bar"
					}
					path := name + ".1.0.0.nupkg"
					token, err := w.st.RemoteCache().
						Begin(w.ctx, types.RemoteCacheKey{RepoKey: w.c.Key, Path: path, Variant: "test"})
					if err != nil {
						t.Fatal(err)
					}
					body := pkg(name, "1.0.0", "<title>New</title>")
					_, err = w.items.Cache(
						w.ctx,
						item.CacheUpload{
							Repo:        w.c,
							Token:       token,
							Kind:        types.RemoteUnique,
							Body:        bytes.NewReader(body),
							Length:      int64(len(body)),
							ContentType: "application/zip",
							RemoteURL:   w.c.URL,
						},
					)
					if err != nil {
						t.Fatal(err)
					}
				case "delete":
					if _, err := w.items.Delete(
						w.ctx,
						item.Target{Repo: w.c, Cached: true, Path: it.FullPath()},
						"tester",
					); err != nil {
						t.Fatal(err)
					}
				case "source":
					changed := w.c
					changed.URL = "https://changed.example"
					if err := w.st.Repos().Update(w.ctx, changed); err != nil {
						t.Fatal(err)
					}
				case "properties":
					if err := w.st.Items().
						SetProperties(w.ctx, it.ID, types.Properties{nuget.PropID: {"wrong"}, "concurrent": {"keep"}}, w.now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				}
			}}
			q := restartedReindexWorker(t, w, &svc)
			err := q.EnsureFresh(w.ctx, format.KindReindex, w.c.Key)
			switch mutation {
			case "source":
				if !errors.Is(err, work.ErrPermanent) {
					t.Fatal(err)
				}
			case "properties":
				if err != nil {
					t.Fatal(err)
				}
				props, err := w.st.Items().Properties(w.ctx, it.ID)
				if err != nil || props["concurrent"][0] != "keep" ||
					props[nuget.PropID][0] != "Foo" {
					t.Fatal(props, err)
				}
			default:
				if !errors.Is(err, store.ErrConflict) {
					t.Fatal("snapshot mutation admitted", err)
				}
				// Cache admission/deletion already applied its own correct hooks. The
				// stale rebuild must not replace those with its earlier inspection.
				expected := w.index(w.c)
				w.now = w.now.Add(time.Hour)
				if err := restartedReindexWorker(
					t,
					w,
					w.items,
				).EnsureFresh(w.ctx, format.KindReindex, w.c.Key); err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(expected, w.index(w.c)); diff != "" {
					t.Fatal(diff)
				}
			}
		})
	}
}

type reindexHookBlobs struct {
	blob.Store
	hook func()
	once sync.Once
}

func (b *reindexHookBlobs) Open(
	ctx context.Context,
	sha string,
) (io.ReadSeekCloser, blob.Info, error) {
	b.once.Do(b.hook)
	return b.Store.Open(ctx, sha) //nolint:wrapcheck // fault/barrier double
}

func TestUnitRemoteReindexCompleteArchiveValidation(t *testing.T) {
	w := newWorld(t, nil)
	w.c = w.repo("remote", repo.Remote, nil)
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	f, _ := z.Create("Foo.nuspec")
	_, _ = io.WriteString(f, nuspecXML("Foo", "1.0.0", ""))
	f, _ = z.CreateHeader(&zip.FileHeader{Name: "payload", Method: zip.Store})
	_, _ = io.WriteString(f, "payload-data")
	_ = z.Close()
	body := buf.Bytes()
	body[bytes.Index(body, []byte("payload-data"))] ^= 1
	// Hashes are correct for these corrupt bytes; nuspec-only indexing accepts
	// them. Remote reindex must check the other entry's CRC before publication.
	cacheReindexPackage(t, w, "Foo.1.0.0.nupkg", body)
	before := w.index(w.c)
	if err := w.items.Reindex(w.ctx, w.c); !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("corrupt payload CRC: %v", err)
	}
	if diff := cmp.Diff(before, w.index(w.c)); diff != "" {
		t.Fatal(diff)
	}
}

func TestUnitRemoteReindexStoreFaults(t *testing.T) {
	for _, tc := range []struct {
		method string
		pass   int
	}{{"Search", 0}, {"Search", 1}, {"PatchProperties", 0}, {"Ensure", 0}, {"PutVersion", 0}} {
		t.Run(fmt.Sprintf("%s-%d", tc.method, tc.pass), func(t *testing.T) {
			w := newWorld(t, nil)
			w.c = w.repo("remote", repo.Remote, nil)
			it := cacheReindexPackage(t, w, "Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", ""))
			if err := w.st.Items().
				SetProperties(w.ctx, it.ID, types.Properties{nuget.PropID: {"wrong"}}, w.now); err != nil {
				t.Fatal(err)
			}
			p, _ := w.st.Packages().Find(w.ctx, w.c.Key, "foo")
			if err := w.st.Packages().Delete(w.ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			svc := *w.items
			svc.Store = &faulty{Store: w.st, f: &faults{pass: map[string]int{tc.method: tc.pass}}}
			if err := svc.Reindex(w.ctx, w.c); !errors.Is(err, errFault) {
				t.Fatal(err)
			}
			props, err := w.st.Items().Properties(w.ctx, it.ID)
			if err != nil || props[nuget.PropID][0] != "wrong" || len(w.index(w.c)) != 0 {
				t.Fatal("partial failure", props, err)
			}
		})
	}
}

func reindexCredential(t *testing.T, w *world) json.RawMessage {
	t.Helper()
	params := auth.HashParams{MemoryKiB: 8, Time: 1, Threads: 1}
	if _, err := w.st.Users().Get(w.ctx, "tester"); errors.Is(err, store.ErrNotFound) {
		hash, err := auth.HashPasswordWith(params, "pw")
		if err != nil {
			t.Fatal(err)
		}
		if err = w.st.Users().
			Create(w.ctx, types.User{Name: "tester", PasswordHash: hash, Admin: true}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := auth.NewAuthenticator(
		w.st.Users(),
		w.st.Settings(),
		auth.Options{Hash: params, Now: func() time.Time { return w.now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Basic(w.ctx, "tester", "pw")
	if err != nil {
		t.Fatal(err)
	}
	w.items.ReindexAuthorize = reindexauth.New(w.st, a)
	reference, err := json.Marshal(p.CredentialReference())
	if err != nil {
		t.Fatal(err)
	}
	return reference
}

func TestUnitRemoteReindexWorkerRevocation(t *testing.T) {
	for _, moment := range []string{"before worker", "before publication"} {
		for _, revoke := range []string{"manage", "password", "disabled"} {
			t.Run(moment+"/"+revoke, func(t *testing.T) {
				w := newWorld(t, nil)
				w.c = w.repo("remote", repo.Remote, nil)
				it := cacheReindexPackage(t, w, "Foo.1.0.0.nupkg", pkg("Foo", "1.0.0", ""))
				reindexCredential(t, w)
				user, err := w.st.Users().Get(w.ctx, "tester")
				if err != nil {
					t.Fatal(err)
				}
				user.Admin = false
				if err = w.st.Users().Update(w.ctx, user); err != nil {
					t.Fatal(err)
				}
				if err = w.st.Permissions().
					Create(w.ctx, types.Permission{Name: "manager", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"tester": types.ActionManage}}, Targets: map[string]types.Target{w.c.Key: {Include: []string{"**"}}}}}}); err != nil {
					t.Fatal(err)
				}
				queueRemoteReindex(t, w)
				queued, err := w.st.WorkQueue().Get(w.ctx, format.KindReindex, w.c.Key)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(queued.Payload, user.PasswordHash) ||
					bytes.Contains(queued.Payload, []byte("$argon2")) {
					t.Fatal("queue contains password material")
				}
				p, _ := w.st.Packages().Find(w.ctx, w.c.Key, "foo")
				if err = w.st.Packages().Delete(w.ctx, p.ID); err != nil {
					t.Fatal(err)
				}
				if err = w.st.Items().
					SetProperties(w.ctx, it.ID, types.Properties{nuget.PropID: {"wrong"}}, w.now); err != nil {
					t.Fatal(err)
				}
				change := func() {
					switch revoke {
					case "manage":
						if err := w.st.Permissions().Delete(w.ctx, "manager"); err != nil {
							t.Fatal(err)
						}
					case "password", "disabled":
						u, err := w.st.Users().Get(w.ctx, "tester")
						if err != nil {
							t.Fatal(err)
						}
						if revoke == "disabled" {
							u.Status = types.UserDisabled
						} else {
							u.PasswordHash = []byte("changed revision")
						}
						if err = w.st.Users().Update(w.ctx, u); err != nil {
							t.Fatal(err)
						}
					}
				}
				svc := *w.items
				if moment == "before worker" {
					change()
				} else {
					svc.Blobs = &reindexHookBlobs{Store: w.items.Blobs, hook: change}
				}
				if err := restartedReindexWorker(
					t,
					w,
					&svc,
				).EnsureFresh(w.ctx, format.KindReindex, w.c.Key); !errors.Is(
					err,
					work.ErrPermanent,
				) {
					t.Fatal("revoked job was not terminal", err)
				}
				failed, err := w.st.WorkQueue().Get(w.ctx, format.KindReindex, w.c.Key)
				if err != nil || failed.State != types.WorkParked {
					t.Fatal(failed, err)
				}
				props, err := w.st.Items().Properties(w.ctx, it.ID)
				if err != nil || props[nuget.PropID][0] != "wrong" || len(w.index(w.c)) != 0 {
					t.Fatal("revoked job published", props, err)
				}
			})
		}
	}
}

func TestUnitRemoteReindexStoredIntegrity(t *testing.T) {
	for _, fault := range []string{"size", "digest", "missing", "no nuspec"} {
		t.Run(fault, func(t *testing.T) {
			w := newWorld(t, nil)
			w.c = w.repo("remote", repo.Remote, nil)
			body := pkg("Foo", "1.0.0", "")
			if fault == "no nuspec" {
				body = zipOf(zipFile{"lib/foo.dll", "payload"})
			}
			it := cacheReindexPackage(t, w, "Foo.1.0.0.nupkg", body)
			var expected error
			switch fault {
			case "size":
				it.Size++
				expected = blob.ErrDigestMismatch
			case "digest":
				it.Actual.SHA512 = strings.Repeat("0", 128)
				expected = blob.ErrDigestMismatch
			case "missing":
				if err := w.items.Blobs.Delete(w.ctx, it.Actual.SHA256); err != nil {
					t.Fatal(err)
				}
				expected = blob.ErrNotFound
			case "no nuspec":
				expected = nuget.ErrNotPackage
			}
			if fault == "size" || fault == "digest" {
				if _, err := w.st.Items().Put(w.ctx, it); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.items.Reindex(w.ctx, w.c); !errors.Is(err, expected) {
				t.Fatalf("%s: %v", fault, err)
			}
		})
	}
}

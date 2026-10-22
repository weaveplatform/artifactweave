package item

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func remoteWorld(t *testing.T) (*world, CacheUpload) {
	t.Helper()
	w := newWorld(t)
	c := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, URL: "https://upstream.example", PackageType: repo.Generic})
	if err := w.db.Repos().Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	token, err := w.db.RemoteCache().
		Begin(context.Background(), types.RemoteCacheKey{RepoKey: c.Key, Path: "a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	return w, CacheUpload{
		Token:       token,
		Repo:        c,
		Body:        strings.NewReader("payload"),
		Length:      7,
		Kind:        types.RemoteUnique,
		By:          "reader",
		ContentType: "application/octet-stream",
	}
}

func TestRemoteCacheIngestion(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	r := &recorder{calls: map[string]bool{}}
	r.deployed = func(ctx context.Context, env format.Env, it types.Item) error {
		if !it.Cached {
			t.Fatal("format did not get cache flag")
		}
		p, err := env.Store.Packages().
			Ensure(ctx, types.Package{RepoKey: it.RepoKey, Name: "package"})
		if err != nil {
			return err
		}
		v, err := env.Store.Packages().
			PutVersion(ctx, types.PackageVersion{PackageID: p.ID, Version: "1.0"})
		if err != nil {
			return err
		}
		return env.Store.Packages().LinkFile(ctx, types.VersionFile{VersionID: v.ID, ItemID: it.ID})
	}
	w.svc.Formats = func(repo.PackageType) format.Handler { return r }
	result, err := w.svc.Cache(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.Actual != sums("payload") || !result.Item.Cached ||
		result.Entry.ItemID != result.Item.ID ||
		result.Entry.Blob.Checksums != result.Item.Actual {
		t.Fatalf("result=%+v", result)
	}
	if r.calls["inspect a.bin"] || !r.calls["deployed a.bin"] {
		t.Fatalf("hook transaction boundaries: %+v", r.calls)
	}
	if w.refs(result.Item.Actual.SHA256) != 1 {
		t.Fatal("wrong item reference count")
	}
	p, err := w.db.Packages().Find(ctx, u.Repo.Key, "package")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := w.db.Packages().Versions(ctx, p.ID)
	if err != nil || len(versions) != 1 {
		t.Fatal("format index missing", err)
	}
	// Another query variant at this path must make the old entry unusable.
	other := u
	other.Token.Key.Variant = "other"
	other.Body = strings.NewReader("variant")
	other.Token, err = w.db.RemoteCache().Begin(ctx, other.Token.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.svc.Cache(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err = w.db.RemoteCache().Read(ctx, u.Token.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old variant served replacement bytes", err)
	}
}

func TestRemoteMetadataOwnsRawBytes(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	u.Kind = types.RemoteMetadata
	u.Body = strings.NewReader("all upstream versions")
	u.Length = -1
	result, err := w.svc.Cache(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.ID != 0 || result.Entry.ItemID != 0 || w.refs(result.Info.Digests.SHA256) != 1 {
		t.Fatalf("metadata result=%+v", result)
	}
	// A locally derived item at the same path has an independent blob.
	if _, err = w.svc.Deploy(
		ctx,
		Deploy{
			Target: Target{Repo: u.Repo, Cached: true, Path: u.Token.Key.Path},
			Body:   strings.NewReader("cached subset"),
		},
	); err != nil {
		t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Read(ctx, u.Token.Key)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := w.svc.Blobs.Open(ctx, entry.Blob.Checksums.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "all upstream versions" {
		t.Fatal("local derived view replaced authoritative source", err)
	}
	if _, err = w.db.RemoteCache().Invalidate(ctx, u.Repo.Key, "", true); err != nil {
		t.Fatal(err)
	}
	entry, err = w.db.RemoteCache().Read(ctx, u.Token.Key)
	if err != nil || !entry.ValidatedAt.IsZero() {
		t.Fatal("metadata expiry", err)
	}
}

func TestRemoteMetadataOptInPublishesItemAndExpiresOnZap(t *testing.T) {
	w, u := remoteWorld(t)
	u.Kind = types.RemoteMetadata
	u.PublishMetadata = true
	result, err := w.svc.Cache(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	if result.Item.ID == 0 || result.Entry.ItemID != result.Item.ID ||
		result.Entry.Kind != types.RemoteMetadata ||
		result.Item.FullPath() != u.Token.Key.Path {
		t.Fatalf("metadata publication: %+v", result)
	}
	if _, err = w.svc.Zap(t.Context(), Target{Repo: u.Repo, Cached: true}); err != nil {
		t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Read(t.Context(), u.Token.Key)
	if err != nil || !entry.ValidatedAt.IsZero() || entry.Blob.Checksums != result.Item.Actual {
		t.Fatalf("zap changed bytes or retained freshness: %+v %v", entry, err)
	}
	if _, err = w.db.Items().GetByID(t.Context(), result.Item.ID); err != nil {
		t.Fatal("zap removed native metadata", err)
	}
}

func TestRemoteChecksumPolicies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   repo.RemoteChecksumPolicy
		declared types.Checksums
		want     error
	}{
		{"generated", repo.GenerateIfAbsent, types.Checksums{}, nil},
		{"matches", repo.Fail, sums("payload"), nil},
		{"missing", repo.Fail, types.Checksums{}, ErrRemoteChecksumRequired},
		{"wrong", repo.GenerateIfAbsent, sums("wrong"), ErrChecksumMismatch},
		{"failwrong", repo.Fail, sums("wrong"), ErrChecksumMismatch},
		{"ignore", repo.IgnoreAndGenerate, sums("wrong"), nil},
		{"pass", repo.PassThru, sums("wrong"), nil},
		{"malformed", repo.PassThru, types.Checksums{SHA256: "invalid"}, ErrBadChecksum},
		{"invalid", repo.RemoteChecksumPolicy("bad"), types.Checksums{}, store.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Repo.RemoteRepoChecksumPolicyType = tc.policy
			u.Declared = tc.declared
			r, err := w.svc.Cache(context.Background(), u)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			if r.Item.Actual != sums("payload") {
				t.Fatal("upstream checksum changed computed identity")
			}
			if tc.policy == repo.PassThru && r.Item.Original != tc.declared {
				t.Fatal("pass-through checksum lost")
			}
		})
	}
}

func TestRemoteCacheRejectsIncompleteAndUnsafe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CacheUpload)
		want   error
	}{
		{"invalidlength", func(u *CacheUpload) { u.Length = -2 }, store.ErrInvalid},
		{"invalidlimit", func(u *CacheUpload) { u.MaxBytes = -1 }, store.ErrInvalid},
		{"short", func(u *CacheUpload) { u.Length = 8 }, ErrRemoteLength},
		{"long", func(u *CacheUpload) { u.Length = 6 }, ErrRemoteLength},
		{"limit", func(u *CacheUpload) { u.MaxBytes = 3 }, ErrRemoteLimit},
		{"disconnect", func(u *CacheUpload) {
			u.Body = io.MultiReader(strings.NewReader("part"), iotest.ErrReader(io.ErrUnexpectedEOF))
		}, io.ErrUnexpectedEOF},
		{"html", func(u *CacheUpload) { u.Body = strings.NewReader("<!DOCTYPE HTML><html>login</html>"); u.Length = -1 }, ErrRemoteMIME},
		{"badtype", func(u *CacheUpload) { u.ContentType = "invalid;" }, ErrRemoteMIME},
		{"denied", func(u *CacheUpload) { u.CheckCurrent = func(context.Context, store.Store) error { return errBoom } }, errBoom},
		{"wrongclass", func(u *CacheUpload) { u.Repo.Class = repo.Local }, store.ErrInvalid},
		{"wrongkey", func(u *CacheUpload) { u.Repo.Key = "elsewhere" }, store.ErrInvalid},
		{"nilbody", func(u *CacheUpload) { u.Body = nil }, store.ErrInvalid},
		{"derived", func(u *CacheUpload) { u.Kind = types.RemoteDerived }, store.ErrInvalid},
		{"badkind", func(u *CacheUpload) { u.Kind = "bad" }, store.ErrInvalid},
		{"badpath", func(u *CacheUpload) { u.Token.Key.Path = "../bad" }, store.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			tc.change(&u)
			_, err := w.svc.Cache(context.Background(), u)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if _, err = w.db.Items().
				Get(context.Background(), "remote", true, "a.bin"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("failed body visible", err)
			}
		})
	}
}

func TestRemoteCacheAtomicFailureAndFence(t *testing.T) {
	for _, fault := range []string{"begin", "commit", "put", "ref", "format", "source", "delete", "zap", "rollback"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			w, u := remoteWorld(t)
			seed, err := w.svc.Cache(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			u.Token, err = w.db.RemoteCache().Begin(ctx, u.Token.Key)
			if err != nil {
				t.Fatal(err)
			}
			u.Body = strings.NewReader("changed")
			switch fault {
			case "begin", "commit":
				w.svc.Blobs = faultyBlobStore{w.svc.Blobs, fault}
			case "put", "ref":
				w.svc.Store = faulty{w.db, fault}
			case "format":
				r := &recorder{
					calls:    map[string]bool{},
					deployed: func(context.Context, format.Env, types.Item) error { return errBoom },
				}
				w.svc.Formats = func(repo.PackageType) format.Handler { return r }
			case "source":
				c := u.Repo
				c.URL = "https://other.example"
				if err = w.db.Repos().Update(ctx, c); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err = w.db.Repos().Delete(ctx, u.Repo.Key); err != nil {
					t.Fatal(err)
				}
				if err = w.db.Repos().Create(ctx, u.Repo); err != nil {
					t.Fatal(err)
				}
			case "zap":
				if _, err = w.db.RemoteCache().Invalidate(ctx, u.Repo.Key, "", false); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "rollback" {
				err = w.db.Tx(ctx, func(ctx context.Context) error {
					if _, err := w.svc.Cache(ctx, u); err != nil {
						return err
					}
					return errBoom
				})
			} else {
				_, err = w.svc.Cache(ctx, u)
			}
			if err == nil {
				t.Fatal("failed/racing write committed")
			}
			it, err := w.db.Items().Get(ctx, u.Repo.Key, true, u.Token.Key.Path)
			if err != nil || it.Actual != seed.Item.Actual {
				t.Fatal("old item changed after failure", err)
			}
			if w.refs(seed.Item.Actual.SHA256) != 1 {
				t.Fatal("old blob reference changed")
			}
		})
	}
}

func TestRemoteNoStore(t *testing.T) {
	for _, mode := range []string{"explicit", "setting", "header", "checksum", "short", "limit", "denied", "cancelled", "copyfailure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			w, u := remoteWorld(t)
			u.NoStore = true
			switch mode {
			case "setting":
				u.NoStore = false
				b := false
				u.Repo.StoreArtifactsLocally = &b
			case "header":
				u.NoStore = false
				u.CacheControl = "private, no-store"
			case "checksum":
				u.Declared = sums("wrong")
			case "short":
				u.Length = 8
			case "limit":
				u.MaxBytes = 3
			case "denied":
				u.CheckCurrent = func(context.Context, store.Store) error { return errBoom }
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "copyfailure":
				u.Body = io.MultiReader(
					strings.NewReader(strings.Repeat("x", 1024)),
					iotest.ErrReader(errBoom),
				)
				u.Length = -1
			}
			r, err := w.svc.Cache(ctx, u)
			if mode == "explicit" || mode == "setting" || mode == "header" {
				if err != nil {
					t.Fatal(err)
				}
				b, err := io.ReadAll(r.Body)
				if err != nil || string(b) != "payload" {
					t.Fatal("transient bytes", err)
				}
				if r.Info.Digests != (blob.Digests{SHA256: sums("payload").SHA256, SHA512: sums("payload").SHA512, SHA1: sums("payload").SHA1, MD5: sums("payload").MD5}) {
					t.Fatal("transient checksums")
				}
				f := r.Body.(transientCache)
				name := f.Name()
				if err = r.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("temporary file survived close")
				}
			} else if err == nil {
				t.Fatal("transient failure returned success")
			}
			if _, err = w.db.Blobs().
				Get(context.Background(), sums("payload").SHA256); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("no-store durable blob index", err)
			}
			if _, err = w.db.Items().
				Get(context.Background(), u.Repo.Key, true, u.Token.Key.Path); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("no-store durable item", err)
			}
			if _, err = w.db.RemoteCache().
				Read(context.Background(), u.Token.Key); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("no-store durable cache", err)
			}
		})
	}
}

// A repository edit must finish while the response is still streaming. The
// completed body then loses its old token, including after delete/recreate ABA.
func TestRemoteCacheStreamingFence(t *testing.T) {
	for _, mode := range []string{"source", "recreate", "invalidate"} {
		t.Run(mode, func(t *testing.T) {
			w, u := remoteWorld(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			body, writer := io.Pipe()
			defer body.Close()
			defer writer.Close()
			u.Body = body
			done := make(chan error, 1)
			go func() { _, err := w.svc.Cache(ctx, u); done <- err }()
			// Peek waits for a complete prefix. A write proves Cache has entered body
			// consumption before the concurrent policy edit begins.
			if _, err := writer.Write([]byte("pay")); err != nil {
				t.Fatal(err)
			}
			changed := make(chan error, 1)
			go func() {
				var err error
				switch mode {
				case "source":
					c := u.Repo
					c.URL = "https://changed.example"
					err = w.db.Repos().Update(ctx, c)
				case "recreate":
					err = w.db.Repos().Delete(ctx, u.Repo.Key)
					if err == nil {
						err = w.db.Repos().Create(ctx, u.Repo)
					}
				case "invalidate":
					_, err = w.db.RemoteCache().Invalidate(ctx, u.Repo.Key, "", false)
				}
				changed <- err
			}()
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("stream held storage transaction")
			}
			if _, err := writer.Write([]byte("load")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, store.ErrConflict) {
					t.Fatal("stale stream committed", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, err := w.db.Items().
				Get(ctx, u.Repo.Key, true, u.Token.Key.Path); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("stale body visible", err)
			}
		})
	}
}

func TestRemoteMetadataCollectionFence(t *testing.T) {
	for _, mode := range []string{"removed", "deleting", "exists-error", "restored"} {
		t.Run(mode, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Kind = types.RemoteMetadata
			u.CheckCurrent = func(ctx context.Context, _ store.Store) error {
				b := types.Blob{Checksums: sums("payload"), Size: 7, Created: at}
				if _, err := w.db.Blobs().MarkDeleting(ctx, b, at); err != nil {
					return err
				}
				if mode == "deleting" {
					return nil
				}
				if mode == "removed" {
					if err := w.svc.Blobs.Delete(ctx, b.Checksums.SHA256); err != nil {
						return err
					}
				}
				return w.db.Blobs().FinishDelete(ctx, b.Checksums.SHA256, at)
			}
			if mode == "exists-error" {
				w.svc.Blobs = faultyBlobStore{w.svc.Blobs, "exists"}
			}
			result, err := w.svc.Cache(context.Background(), u)
			if mode == "restored" {
				if err != nil || result.Entry.Blob.Checksums != sums("payload") {
					t.Fatal("restored bytes refused", err)
				}
				return
			}
			want := ErrCollected
			if mode == "deleting" {
				want = store.ErrDeleting
			}
			if mode == "exists-error" {
				want = errBoom
			}
			if !errors.Is(err, want) {
				t.Fatal("collection race", err)
			}
			if _, err = w.db.RemoteCache().
				Read(context.Background(), u.Token.Key); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("collected metadata visible", err)
			}
		})
	}
}

func TestRemoteMIMERules(t *testing.T) {
	for _, mode := range []string{"disabled", "override", "mismatch", "zip-alias", "gzip-alias", "empty"} {
		t.Run(mode, func(t *testing.T) {
			w, u := remoteWorld(t)
			switch mode {
			case "disabled":
				f := false
				u.Repo.BlockMismatchingMimeTypes = &f
				u.Body = strings.NewReader("<html>error</html>")
				u.Length = -1
			case "override":
				u.Repo.MismatchingMimeTypesOverrideList = " text/html "
				u.Body = strings.NewReader("<html>accepted</html>")
				u.ContentType = "text/html"
				u.Length = -1
			case "mismatch":
				u.Token.Key.Path = "a.json"
				u.ContentType = "image/png"
			case "zip-alias":
				u.Token.Key.Path = "a.jar"
				u.ContentType = "application/zip"
			case "gzip-alias":
				u.Token.Key.Path = "a.gz"
				u.ContentType = "application/x-gzip"
			case "empty":
				u.Body = strings.NewReader("")
				u.Length = 0
				u.ContentType = ""
			}
			var err error
			u.Token, err = w.db.RemoteCache().Begin(context.Background(), u.Token.Key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.svc.Cache(context.Background(), u)
			if mode == "mismatch" {
				if !errors.Is(err, ErrRemoteMIME) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A trusted adapter can identify extensionless HTML metadata; an untrusted
// upstream cannot turn an artifact or JSON response into accepted HTML.
func TestRemoteExpectedMetadataMIME(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		kind           types.RemoteKind
		want           error
	}{
		{"html-index", "text/html", types.RemoteMetadata, nil},
		{"artifact-masquerade", "", types.RemoteUnique, ErrRemoteMIME},
		{"json-masquerade", "application/json", types.RemoteMetadata, ErrRemoteMIME},
		{"artifact-hint", "text/html", types.RemoteUnique, ErrRemoteMIME},
		{"wildcard", "text/*", types.RemoteMetadata, ErrRemoteMIME},
		{"invalid", "broken;", types.RemoteMetadata, ErrRemoteMIME},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.ExpectedContentType = tc.expected
			u.Kind = tc.kind
			u.ContentType = "text/html; charset=utf-8"
			u.Body = strings.NewReader("<!DOCTYPE html><html><a href='pkg.whl'>pkg</a></html>")
			u.Length = -1
			result, err := w.svc.Cache(context.Background(), u)
			if !errors.Is(err, tc.want) {
				t.Fatalf("MIME: %v want %v", err, tc.want)
			}
			if err == nil && result.Entry.Kind != types.RemoteMetadata {
				t.Fatal("HTML did not stay raw metadata")
			}
			if err != nil {
				if _, err = w.db.RemoteCache().
					Read(context.Background(), u.Token.Key); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatal("masquerading body cached", err)
				}
			}
		})
	}
}

func TestRemoteAdapterValidationAndPins(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("transient=%v/reject=%v", noStore, reject), func(t *testing.T) {
				w, u := remoteWorld(t)
				u.NoStore = noStore
				u.Repo.RemoteRepoChecksumPolicyType = repo.IgnoreAndGenerate
				u.Expected = sums("payload")
				calls := 0
				u.Validate = func(ctx context.Context, r io.ReadSeeker, size int64) error {
					calls++
					outside := false
					w.db.AfterCommit(ctx, func() { outside = true })
					if !outside {
						t.Fatal("validation ran inside SQL")
					}
					if _, err := w.db.RemoteCache().
						Read(ctx, u.Token.Key); !errors.Is(
						err,
						store.ErrNotFound,
					) {
						t.Fatal("published before validation", err)
					}
					b, err := io.ReadAll(r)
					if err != nil || string(b) != "payload" || size != 7 {
						t.Fatalf("validation body %q %d %v", b, size, err)
					}
					if reject {
						return ErrRemoteMIME
					}
					return nil
				}
				out, err := w.svc.Cache(t.Context(), u)
				if calls != 1 {
					t.Fatal("validation calls", calls)
				}
				if reject {
					if !errors.Is(err, ErrRemoteMIME) {
						t.Fatal(err)
					}
					if _, err = w.db.Items().
						Get(t.Context(), u.Repo.Key, true, u.Token.Key.Path); !errors.Is(
						err,
						store.ErrNotFound,
					) {
						t.Fatal("rejected body published", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if out.Body != nil {
					defer out.Body.Close()
					b, err := io.ReadAll(out.Body)
					if err != nil || string(b) != "payload" {
						t.Fatal("validator did not rewind body", err)
					}
				}
			})
		}
		for _, policy := range []repo.RemoteChecksumPolicy{repo.IgnoreAndGenerate, repo.PassThru} {
			w, u := remoteWorld(t)
			u.NoStore, u.Repo.RemoteRepoChecksumPolicyType = noStore, policy
			u.Expected = sums("different")
			if _, err := w.svc.Cache(t.Context(), u); !errors.Is(err, ErrChecksumMismatch) {
				t.Fatal("metadata pin ignored", policy, noStore, err)
			}
		}
	}
}

func TestRemoteJSONMetadataMIMECompatibility(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		good        bool
	}{
		{"application/json", true},
		{"application/vnd.npm.install-v1+json; charset=utf-8", true},
		{"application/problem+json", true},
		{"text/custom+json", false},
		{"text/html", false},
		{"application/xhtml+xml", false},
		{"application/json; broken", false},
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Kind, u.ExpectedContentType, u.ContentType = types.RemoteMetadata, "application/json", tc.contentType
			u.Body, u.Length = strings.NewReader(`{"versions":{}}`), 15
			_, err := w.svc.Cache(t.Context(), u)
			if (err == nil) != tc.good {
				t.Fatalf("MIME compatibility: %v", err)
			}
		})
	}
}

func TestRemoteMetadataMIMEAlternatives(t *testing.T) {
	for _, tc := range []struct {
		media, body string
		good        bool
	}{
		{"application/json", `{"files":[]}`, true},
		{"application/vnd.pypi.simple.v1+json", `{"files":[]}`, true},
		{"text/html", `<!DOCTYPE html><html></html>`, true},
		{"application/vnd.pypi.simple.v1+html", `<!DOCTYPE html><html></html>`, true},
		{"application/json", `<!DOCTYPE html><html></html>`, false},
		{"text/html", `{"files":[]}`, false},
		{"application/problem+json", `{"files":[]}`, false},
		{"text/plain", `{"files":[]}`, false},
		{"application/json; bad", `{"files":[]}`, false},
	} {
		t.Run(tc.media+tc.body, func(t *testing.T) {
			w, u := remoteWorld(t)
			u.Kind = types.RemoteMetadata
			u.ExpectedContentTypes = []string{
				"application/json",
				"application/vnd.pypi.simple.v1+json",
				"text/html",
				"application/vnd.pypi.simple.v1+html",
			}
			u.ContentType = tc.media
			u.Body = strings.NewReader(tc.body)
			u.Length = int64(len(tc.body))
			u.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
			_, err := w.svc.Cache(t.Context(), u)
			if (err == nil) != tc.good {
				t.Fatalf("MIME alternatives %v", err)
			}
		})
	}
	for _, bad := range [][]string{{"text/html", "text/html"}, {"Text/HTML"}, {"text/*"}, {"text/html; charset=utf-8"}, {"invalid"}} {
		w, u := remoteWorld(t)
		u.Kind = types.RemoteMetadata
		u.ExpectedContentTypes = bad
		u.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
		if _, err := w.svc.Cache(t.Context(), u); !errors.Is(err, ErrRemoteMIME) {
			t.Fatalf("accepted invalid alternatives %v: %v", bad, err)
		}
	}
	for _, change := range []func(*CacheUpload){func(u *CacheUpload) { u.Kind = types.RemoteUnique }, func(u *CacheUpload) { u.Validate = nil }, func(u *CacheUpload) { u.ExpectedContentType = "text/html" }} {
		w, u := remoteWorld(t)
		u.Kind = types.RemoteMetadata
		u.ExpectedContentTypes = []string{"text/html"}
		u.Validate = func(context.Context, io.ReadSeeker, int64) error { return nil }
		change(&u)
		if _, err := w.svc.Cache(t.Context(), u); !errors.Is(err, ErrRemoteMIME) {
			t.Fatal("unsafe alternative hint", err)
		}
	}
}

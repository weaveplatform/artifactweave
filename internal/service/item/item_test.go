package item

import (
	"context"
	"crypto/md5"  //nolint:gosec // MD5 is a wire checksum here
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum here
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var at = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

type world struct {
	t   *testing.T
	db  *inmem.Store
	svc *Service
	loc repo.Config
}

func newWorld(t *testing.T) *world {
	t.Helper()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	loc := *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local})
	if err := db.Repos().Create(context.Background(), loc); err != nil {
		t.Fatal(err)
	}
	return &world{
		t: t, db: db, loc: loc,
		svc: &Service{Store: db, Blobs: blobs, Now: func() time.Time { return at }},
	}
}

func sums(body string) types.Checksums {
	s1 := sha1.Sum([]byte(body)) //nolint:gosec // wire checksum
	s2 := sha256.Sum256([]byte(body))
	s5 := sha512.Sum512([]byte(body))
	m := md5.Sum([]byte(body)) //nolint:gosec // wire checksum
	return types.Checksums{
		SHA256: hex.EncodeToString(s2[:]),
		SHA512: hex.EncodeToString(s5[:]),
		SHA1:   hex.EncodeToString(s1[:]),
		MD5:    hex.EncodeToString(m[:]),
	}
}

func (w *world) deploy(p, body string, declared types.Checksums) (types.Item, error) {
	return w.svc.Deploy(context.Background(), Deploy{
		Target: Target{Repo: w.loc, Path: p}, Body: strings.NewReader(body),
		Declared: declared, By: "alice",
	})
}

func (w *world) refs(sha string) int64 {
	w.t.Helper()
	b, err := w.db.Blobs().Get(context.Background(), sha)
	if err != nil {
		w.t.Fatal(err)
	}
	return b.Refs
}

func TestDeploy(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("hello")
	it, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "a/b/hello.txt"}, Body: strings.NewReader("hello"),
		Declared:   types.Checksums{SHA1: strings.ToUpper(c.SHA1)},
		Properties: types.Properties{"k": {"v"}}, By: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The SHA-256 the client did not declare is recorded as its own.
	if it.Size != 5 || it.Actual != c ||
		it.Original != (types.Checksums{SHA1: c.SHA1, SHA256: c.SHA256}) ||
		it.MimeType != "text/plain" || it.CreatedBy != "alice" || !it.Created.Equal(at) {
		t.Fatalf("item %+v", it)
	}
	for _, p := range []string{"a", "a/b"} {
		f, err := w.db.Items().Get(ctx, "libs", false, p)
		if err != nil || !f.Folder {
			t.Fatalf("parent %s: %+v %v", p, f, err)
		}
	}
	if props, _ := w.db.Items().Properties(ctx, it.ID); props["k"][0] != "v" {
		t.Fatalf("properties %v", props)
	}
	if w.refs(c.SHA256) != 1 {
		t.Fatal("blob not referenced once")
	}

	// Overwriting keeps the creation, references the new content and
	// releases the old.
	w.svc.Now = func() time.Time { return at.Add(time.Hour) }
	again, err := w.deploy("a/b/hello.txt", "bye", types.Checksums{})
	if err != nil || again.ID != it.ID || !again.Created.Equal(at) ||
		!again.Modified.Equal(at.Add(time.Hour)) {
		t.Fatalf("overwrite %+v %v", again, err)
	}
	if w.refs(c.SHA256) != 0 || w.refs(sums("bye").SHA256) != 1 {
		t.Fatal("overwrite did not move the reference")
	}
}

func TestDeployChecksums(t *testing.T) {
	w := newWorld(t)
	wrong := types.Checksums{SHA1: strings.Repeat("0", 40)}
	if _, err := w.deploy("x.bin", "data", wrong); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("client policy mismatch: %v", err)
	}
	if _, err := w.db.Items().
		Get(context.Background(), "libs", false, "x.bin"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("a rejected deploy stored the item")
	}
	if _, err := w.deploy(
		"x.bin",
		"data",
		types.Checksums{MD5: "xyz"},
	); !errors.Is(
		err,
		ErrBadChecksum,
	) {
		t.Fatalf("malformed: %v", err)
	}
	// Under server-generated-checksums the declared value is only recorded.
	w.loc.ChecksumPolicyType = repo.ServerGeneratedChecksums
	it, err := w.deploy("x.bin", "data", wrong)
	if err != nil || it.Original.SHA1 != wrong.SHA1 || it.Actual.SHA1 != sums("data").SHA1 {
		t.Fatalf("server policy: %+v %v", it, err)
	}
}

// recordingBlobStore records the expected SHA-256 of every BeginUpload.
type recordingBlobStore struct {
	blob.Store
	expected *[]string
}

func (r recordingBlobStore) BeginUpload(
	ctx context.Context,
	o ...blob.UploadOption,
) (blob.Upload, error) {
	opts, err := blob.ResolveOptions(o...)
	if err != nil {
		return nil, err //nolint:wrapcheck // test fake
	}
	*r.expected = append(*r.expected, opts.ExpectedSHA256)
	return r.Store.BeginUpload(ctx, o...) //nolint:wrapcheck // test fake
}

// TestDeployDeclaresSHA256 checks that an enforced declared SHA-256 reaches
// the blob store, so object stores write the content key directly.
func TestDeployDeclaresSHA256(t *testing.T) {
	w := newWorld(t)
	var expected []string
	w.svc.Blobs = recordingBlobStore{w.svc.Blobs, &expected}
	ctx := context.Background()
	c := sums("data")

	if _, err := w.deploy("ok.bin", "data", types.Checksums{SHA256: c.SHA256}); err != nil {
		t.Fatal(err)
	}
	// A wrong declaration is refused as before, and nothing is stored.
	wrong := strings.Repeat("0", 64)
	if _, err := w.deploy("bad.bin", "other", types.Checksums{SHA256: wrong}); !errors.Is(
		err, ErrChecksumMismatch,
	) {
		t.Fatalf("wrong sha256: %v", err)
	}
	if ok, err := w.svc.Blobs.Exists(ctx, sums("other").SHA256); err != nil || ok {
		t.Fatalf("a rejected deploy stored its content: %v %v", ok, err)
	}
	if _, err := w.db.Items().Get(ctx, "libs", false, "bad.bin"); !errors.Is(
		err, store.ErrNotFound,
	) {
		t.Fatal("a rejected deploy stored the item")
	}
	// Only a SHA-1 declared: nothing to pass.
	if _, err := w.deploy("sha1.bin", "data", types.Checksums{SHA1: c.SHA1}); err != nil {
		t.Fatal(err)
	}
	// Under server-generated checksums a mismatch is only recorded, so the
	// declared digest must not reach the blob store.
	w.loc.ChecksumPolicyType = repo.ServerGeneratedChecksums
	it, err := w.deploy("server.bin", "data", types.Checksums{SHA256: wrong})
	if err != nil || it.Original.SHA256 != wrong || it.Actual.SHA256 != c.SHA256 {
		t.Fatalf("server policy: %+v %v", it, err)
	}
	if want := []string{c.SHA256, wrong, "", ""}; !slices.Equal(expected, want) {
		t.Fatalf("BeginUpload saw expected digests %q, want %q", expected, want)
	}
}

func TestDeployConflicts(t *testing.T) {
	w := newWorld(t)
	if _, err := w.deploy("dir/file", "x", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("dir", "x", types.Checksums{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("file over folder: %v", err)
	}
	if _, err := w.deploy("dir/file/inner", "x", types.Checksums{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("file below a file: %v", err)
	}
	if _, err := w.svc.MakeFolder(
		context.Background(),
		Target{Repo: w.loc, Path: "dir/file"},
		nil,
		"a",
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("folder over file: %v", err)
	}
}

func TestDeployIfAbsent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	once := func(p, body string) (types.Item, error) {
		return w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: p}, Body: strings.NewReader(body), IfAbsent: true,
		})
	}
	if _, err := once("pkg/-/pkg-1.0.0.tgz", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := once("pkg/-/pkg-1.0.0.tgz", "second"); !errors.Is(err, ErrExists) {
		t.Fatalf("over a file: %v", err)
	}
	if _, err := once("pkg", "x"); !errors.Is(err, ErrExists) {
		t.Fatalf("over a folder: %v", err)
	}
	got, err := w.db.Items().Get(ctx, w.loc.Key, false, "pkg/-/pkg-1.0.0.tgz")
	if err != nil || got.Actual.SHA256 != sums("first").SHA256 {
		t.Fatalf("the first file was replaced: %+v %v", got, err)
	}
	if b, err := w.db.Blobs().Get(ctx, sums("second").SHA256); err == nil && b.Refs != 0 {
		t.Fatalf("refused content referenced: %+v", b)
	}
}

func TestDeclaresContentOf(t *testing.T) {
	f := types.Item{Actual: sums("content")}
	c, o := sums("content"), sums("other")
	for _, tc := range []struct {
		declared types.Checksums
		want     bool
	}{
		{types.Checksums{SHA1: c.SHA1}, true},
		{types.Checksums{SHA256: c.SHA256}, true},
		{types.Checksums{SHA1: c.SHA1, SHA256: c.SHA256, MD5: o.MD5}, true},
		{types.Checksums{}, false},
		{types.Checksums{MD5: c.MD5, SHA512: c.SHA512}, false},
		{types.Checksums{SHA1: c.SHA1, SHA256: o.SHA256}, false},
		{types.Checksums{SHA1: o.SHA1, SHA256: c.SHA256}, false},
	} {
		if got := DeclaresContentOf(tc.declared, f); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.declared, got, tc.want)
		}
	}
}

// TestDeployOverwriteIdentical: without the right to overwrite, a deploy
// replaces a file only when it declares the file's content and stores
// exactly it; the stored bytes decide, not the declaration.
func TestDeployOverwriteIdentical(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("content")
	// Declarations are only recorded here, so a false one gets as far as
	// the overwrite check.
	w.loc.ChecksumPolicyType = repo.ServerGeneratedChecksums
	if _, err := w.deploy("f", "content", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("other", "other", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	deploy := func(body string, declared types.Checksums, identical bool) error {
		_, err := w.svc.Deploy(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: "f"}, Body: strings.NewReader(body),
			Declared: declared, By: "bob", OverwriteIdentical: identical,
			CanOverwritePath: func(string) bool { return false },
		})
		return err
	}
	for name, tc := range map[string]struct {
		body      string
		declared  types.Checksums
		identical bool
		ok        bool
	}{
		"same content":            {"content", types.Checksums{SHA1: c.SHA1}, true, true},
		"not allowed":             {"content", types.Checksums{SHA1: c.SHA1}, false, false},
		"nothing declared":        {"content", types.Checksums{}, true, false},
		"other bytes":             {"other", types.Checksums{SHA1: c.SHA1}, true, false},
		"one declaration differs": {"content", types.Checksums{SHA1: c.SHA1, SHA256: sums("x").SHA256}, true, false},
	} {
		err := deploy(tc.body, tc.declared, tc.identical)
		var denied *OverwriteDeniedError
		if tc.ok != (err == nil) || !tc.ok && (!errors.As(err, &denied) ||
			*denied != (OverwriteDeniedError{Repo: "libs", Path: "f", User: "bob"})) {
			t.Errorf("%s: %v", name, err)
		}
	}
	got, err := w.db.Items().Get(ctx, w.loc.Key, false, "f")
	if err != nil || got.Actual != c || got.ModifiedBy != "bob" {
		t.Fatalf("file %+v %v", got, err)
	}
	// A checksum deploy is held to the same rule, with the content it found.
	byChecksum := func(declared types.Checksums) error {
		_, err := w.svc.DeployByChecksum(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: "f"}, Declared: declared, OverwriteIdentical: true,
			CanOverwritePath: func(string) bool { return false },
		})
		return err
	}
	if err := byChecksum(types.Checksums{SHA256: c.SHA256}); err != nil {
		t.Fatalf("same content by checksum: %v", err)
	}
	err = byChecksum(types.Checksums{SHA1: sums("other").SHA1})
	if !errors.Is(err, ErrOverwriteDenied) {
		t.Fatalf("other content by checksum: %v", err)
	}
}

// The refusal reads as Artifactory 7.x's (RTFACT-26398, a 7.26.0 REST
// deploy), which clients match on "overwrite artifact", and is still
// ErrOverwriteDenied to a caller that tests for that.
func TestOverwriteDeniedText(t *testing.T) {
	for _, c := range []struct {
		err  *OverwriteDeniedError
		want string
	}{
		{
			&OverwriteDeniedError{Repo: "test-libs-release-local", Path: "abc-test-art-plugin-0.3.jar", User: "abc@jfrog.com"},
			"Not enough permissions to delete/overwrite artifact " +
				"'test-libs-release-local:abc-test-art-plugin-0.3.jar' " +
				"(user: 'abc@jfrog.com' needs DELETE permission).",
		},
		{
			&OverwriteDeniedError{Repo: "pypi-local", Path: "pkg/1.0/pkg-1.0.tar.gz", User: "anonymous"},
			"Not enough permissions to delete/overwrite artifact 'pypi-local:pkg/1.0/pkg-1.0.tar.gz' " +
				"(user: 'anonymous' needs DELETE permission).",
		},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("text %q, want %q", got, c.want)
		}
		if !errors.Is(fmt.Errorf("deploy: %w", c.err), ErrOverwriteDenied) {
			t.Errorf("%v is not ErrOverwriteDenied", c.err)
		}
	}
}

func TestDeployUploadFails(t *testing.T) {
	w := newWorld(t)
	_, err := w.svc.Deploy(context.Background(), Deploy{
		Target: Target{Repo: w.loc, Path: "x"}, Body: iotest.ErrReader(errors.New("cut")),
	})
	if err == nil || !strings.Contains(err.Error(), "upload") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeployByChecksum(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("shared content")
	if _, err := w.deploy("first", "shared content", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	byChecksum := func(p string, d types.Checksums) (types.Item, error) {
		return w.svc.DeployByChecksum(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: p}, Declared: d, By: "bob",
			Properties: types.Properties{"via": {"checksum"}},
		})
	}
	for name, d := range map[string]types.Checksums{
		"sha1": {SHA1: c.SHA1}, "sha256": {SHA256: c.SHA256}, "md5": {MD5: c.MD5}, "all": c,
	} {
		// Every checksum of the content is recorded as the client's, however
		// few the deploy named.
		it, err := byChecksum("copies/"+name, d)
		if err != nil || it.Actual != c || it.Original != c ||
			it.Size != int64(len("shared content")) {
			t.Fatalf("%s: %+v %v", name, it, err)
		}
	}
	if w.refs(c.SHA256) != 5 {
		t.Fatalf("refs %d, want 5", w.refs(c.SHA256))
	}
	for name, d := range map[string]types.Checksums{
		"unknown":       {SHA1: strings.Repeat("1", 40)},
		"one disagrees": {SHA256: c.SHA256, MD5: strings.Repeat("2", 32)},
	} {
		if _, err := byChecksum("miss", d); !errors.Is(err, ErrNoBlob) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := byChecksum("miss", types.Checksums{}); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("no checksum: %v", err)
	}
	if _, err := byChecksum("miss", types.Checksums{SHA1: "zz"}); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("malformed: %v", err)
	}
	// The index can outlive the content once garbage collection removed it.
	if err := w.svc.Blobs.Delete(ctx, c.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := byChecksum("gone", types.Checksums{SHA256: c.SHA256}); !errors.Is(err, ErrNoBlob) {
		t.Fatalf("content gone: %v", err)
	}
}

// TestDeployByChecksumReusable: the caller's right to the content decides,
// once the content is found; a refusal is the same ErrNoBlob as a miss, so
// it does not tell the caller the content exists.
func TestDeployByChecksumReusable(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("guarded content")
	if _, err := w.deploy("first", "guarded content", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	var asked []types.Blob
	byChecksum := func(p string, ok bool, err error) (types.Item, error) {
		return w.svc.DeployByChecksum(ctx, Deploy{
			Target: Target{Repo: w.loc, Path: p}, Declared: types.Checksums{SHA1: c.SHA1},
			Reusable: func(_ context.Context, b types.Blob) (bool, error) {
				asked = append(asked, b)
				return ok, err
			},
		})
	}
	if _, err := byChecksum("refused", false, nil); !errors.Is(err, ErrNoBlob) {
		t.Fatalf("refused: %v", err)
	}
	_, err := w.db.Items().Get(ctx, w.loc.Key, false, "refused")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused deploy stored an item: %v", err)
	}
	if _, err := byChecksum("failed", false, errBoom); !errors.Is(err, errBoom) ||
		errors.Is(err, ErrNoBlob) {
		t.Fatalf("a failing check is an error, not a miss: %v", err)
	}
	if it, err := byChecksum("allowed", true, nil); err != nil || it.Actual != c {
		t.Fatalf("allowed: %+v %v", it, err)
	}
	if len(asked) != 3 || asked[0].Checksums != c {
		t.Fatalf("asked about %+v", asked)
	}
	// A checksum that finds nothing never asks.
	asked = nil
	_, err = w.svc.DeployByChecksum(ctx, Deploy{
		Target:   Target{Repo: w.loc, Path: "miss"},
		Declared: types.Checksums{SHA1: strings.Repeat("1", 40)},
		Reusable: func(context.Context, types.Blob) (bool, error) {
			asked = append(asked, types.Blob{})
			return true, nil
		},
	})
	if !errors.Is(err, ErrNoBlob) || len(asked) != 0 {
		t.Fatalf("a miss: %v, asked %d times", err, len(asked))
	}
}

func TestDeployCommitted(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("committed")
	u, err := w.svc.Blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, strings.NewReader("committed")); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	it, err := w.svc.DeployCommitted(ctx, Deploy{
		Target:   Target{Repo: w.loc, Path: "img/_uploads/x"},
		Declared: types.Checksums{SHA256: c.SHA256}, By: "carol",
	}, info)
	if err != nil || it.Actual != c || it.Original != (types.Checksums{SHA256: c.SHA256}) ||
		it.Size != int64(len("committed")) || it.CreatedBy != "carol" {
		t.Fatalf("%+v %v", it, err)
	}
	if w.refs(c.SHA256) != 1 {
		t.Fatal("content not referenced once")
	}
	if _, err := w.svc.DeployCommitted(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "y"}, Declared: types.Checksums{SHA256: "zz"},
	}, info); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("malformed declared checksum: %v", err)
	}
}

func TestMakeFolder(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	f, err := w.svc.MakeFolder(
		ctx,
		Target{Repo: w.loc, Path: "x/y/"},
		types.Properties{"p": {"1"}},
		"alice",
	)
	if err != nil || !f.Folder || f.FullPath() != "x/y" {
		t.Fatalf("folder %+v %v", f, err)
	}
	again, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "x/y"}, nil, "bob")
	if err != nil || again.ID != f.ID || again.CreatedBy != "alice" {
		t.Fatalf("existing folder %+v %v", again, err)
	}
	if props, _ := w.db.Items().Properties(ctx, f.ID); props["p"][0] != "1" {
		t.Fatalf("properties %v", props)
	}
}

func TestEnsureRoot(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	root, err := w.svc.EnsureRoot(ctx, Target{Repo: w.loc}, "alice")
	if err != nil || !root.IsRoot() || root.Cached || !root.Created.Equal(at) ||
		root.CreatedBy != "alice" || root.ModifiedBy != "alice" {
		t.Fatalf("root %+v %v", root, err)
	}
	again, err := w.svc.EnsureRoot(ctx, Target{Repo: w.loc}, "bob")
	if err != nil || again.ID != root.ID || again.CreatedBy != "alice" {
		t.Fatalf("existing root %+v %v", again, err)
	}
	if _, err := w.svc.EnsureRoot(ctx, Target{}, "alice"); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no repository: %v", err)
	}
	// Deleting the repository's content leaves its root alone.
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc}, "x"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.db.Items().Root(ctx, "libs", false); err != nil || got.ID != root.ID {
		t.Fatalf("root after a content delete %+v %v", got, err)
	}
}

func TestRecordChecksum(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("jar")
	if _, err := w.deploy("lib.jar", "jar", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.MakeFolder(ctx, Target{Repo: w.loc, Path: "dir"}, nil, "a"); err != nil {
		t.Fatal(err)
	}
	tgt := Target{Repo: w.loc, Path: "lib.jar"}
	it, err := w.svc.RecordChecksum(ctx, tgt, SHA1, strings.ToUpper(c.SHA1))
	if err != nil || it.Original.SHA1 != c.SHA1 {
		t.Fatalf("matching: %+v %v", it, err)
	}
	wrong := strings.Repeat("3", 32)
	if _, err := w.svc.RecordChecksum(ctx, tgt, MD5, wrong); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("client policy mismatch: %v", err)
	}
	if _, err := w.svc.RecordChecksum(ctx, tgt, SHA256, "nothex"); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("malformed: %v", err)
	}
	if _, err := w.svc.RecordChecksum(
		ctx,
		Target{Repo: w.loc, Path: "dir"},
		SHA1,
		c.SHA1,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("folder: %v", err)
	}
	if _, err := w.svc.RecordChecksum(
		ctx,
		Target{Repo: w.loc, Path: "none"},
		SHA1,
		c.SHA1,
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("missing: %v", err)
	}
	tgt.Repo.ChecksumPolicyType = repo.ServerGeneratedChecksums
	if it, err := w.svc.RecordChecksum(
		ctx,
		tgt,
		MD5,
		wrong,
	); err != nil ||
		it.Original.MD5 != wrong {
		t.Fatalf("server policy: %+v %v", it, err)
	}
}

func TestDeleteToTrash(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("one")
	if _, err := w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "d/one"}, Body: strings.NewReader("one"),
		Properties: types.Properties{"k": {"v"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.deploy("d/sub/two", "two", types.Checksums{}); err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "d"}, "carol")
	if err != nil || res.Files != 2 || res.Folders != 2 {
		t.Fatalf("delete %+v %v", res, err)
	}
	entries, _, err := w.db.Trash().List(ctx, "libs", "", store.Page{})
	if err != nil || len(entries) != 4 {
		t.Fatalf("trash %v %v", entries, err)
	}
	for _, e := range entries {
		if e.DeletedBy != "carol" || !e.DeletedAt.Equal(at) {
			t.Fatalf("entry %+v", e)
		}
		if e.Record.Item.Name == "one" && e.Record.Properties["k"][0] != "v" {
			t.Fatalf("trash lost the properties: %+v", e.Record)
		}
	}
	// The trash keeps the content referenced for a restore.
	if w.refs(c.SHA256) != 1 {
		t.Fatal("trashed content lost its reference")
	}
	if _, err := w.svc.Delete(
		ctx,
		Target{Repo: w.loc, Path: "d"},
		"carol",
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteWithoutTrash(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(*world) Target{
		"trash off": func(w *world) Target {
			if err := w.db.Settings().Put(ctx, types.SettingTrashcan, types.TrashcanSettings{}); err != nil {
				t.Fatal(err)
			}
			return Target{Repo: w.loc, Path: "f"}
		},
		"remote cache": func(w *world) Target {
			return Target{Repo: w.loc, Cached: true, Path: "f"}
		},
	} {
		w := newWorld(t)
		tgt := setup(w)
		if _, err := w.svc.Deploy(
			ctx,
			Deploy{Target: tgt, Body: strings.NewReader("gone")},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.Delete(ctx, tgt, "x"); err != nil {
			t.Fatal(err)
		}
		entries, _, _ := w.db.Trash().List(ctx, "", "", store.Page{})
		if len(entries) != 0 || w.refs(sums("gone").SHA256) != 0 {
			t.Fatalf("%s: trash %v, refs %d", name, entries, w.refs(sums("gone").SHA256))
		}
	}
}

// TestPurge deletes for good with the trash can on.
func TestPurge(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	tgt := Target{Repo: w.loc, Path: "f"}
	if _, err := w.svc.Deploy(
		ctx,
		Deploy{Target: tgt, Body: strings.NewReader("gone")},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Purge(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	entries, _, _ := w.db.Trash().List(ctx, "", "", store.Page{})
	if len(entries) != 0 || w.refs(sums("gone").SHA256) != 0 {
		t.Fatalf("trash %v, refs %d", entries, w.refs(sums("gone").SHA256))
	}
}

func TestDeleteRoot(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for _, p := range []string{"a", "b/c"} {
		if _, err := w.deploy(p, p, types.Checksums{}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := w.svc.Delete(ctx, Target{Repo: w.loc}, "x")
	if err != nil || res.Files != 2 {
		t.Fatalf("root delete %+v %v", res, err)
	}
	if res, err := w.svc.Delete(ctx, Target{Repo: w.loc}, "x"); err != nil || res.Files != 0 {
		t.Fatalf("empty root delete %+v %v", res, err)
	}
}

func TestTrashSettingUnreadable(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.db.Settings().Put(ctx, types.SettingTrashcan, "not an object"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "x"}, "x"); err == nil {
		t.Fatal("an unreadable setting was ignored")
	}
}

func TestSidecarAndMimeType(t *testing.T) {
	for p, want := range map[string][2]string{
		"a/b.jar.sha1": {"a/b.jar", "sha1"}, "b.sha256": {"b", "sha256"}, "c.md5": {"c", "md5"},
		"d.tgz.sha512": {"d.tgz", "sha512"},
	} {
		base, alg, ok := Sidecar(p)
		if !ok || base != want[0] || string(alg) != want[1] {
			t.Errorf("Sidecar(%q) = %q %q %v", p, base, alg, ok)
		}
	}
	for _, p := range []string{"file.jar", ".sha1", "dir/.md5", "dir/.sha512", "x.sha384"} {
		if _, _, ok := Sidecar(p); ok {
			t.Errorf("Sidecar(%q) matched", p)
		}
	}
	c := sums("x")
	if SHA1.Of(c) != c.SHA1 || SHA256.Of(c) != c.SHA256 || SHA512.Of(c) != c.SHA512 ||
		MD5.Of(c) != c.MD5 {
		t.Fatal("Algorithm.Of")
	}
	if MimeType("A.JAR") != "application/java-archive" ||
		MimeType("x.unknown") != "application/octet-stream" {
		t.Fatal("MimeType")
	}
}

// TestServedChecksum: under client-checksums a sidecar serves the client's
// checksum, or nothing; the computed one under server-generated-checksums,
// for Maven metadata and in a remote cache.
func TestServedChecksum(t *testing.T) {
	c := sums("x")
	client := *repo.Defaults(&repo.Config{Key: "libs", Class: repo.Local})
	server := client
	server.ChecksumPolicyType = repo.ServerGeneratedChecksums
	remote := *repo.Defaults(&repo.Config{Key: "up", Class: repo.Remote})
	wrong := strings.Repeat("0", 40)
	file := types.Item{Name: "app.jar", Actual: c, Original: types.Checksums{SHA1: wrong}}
	for name, tc := range map[string]struct {
		c    repo.Config
		it   types.Item
		a    Algorithm
		want string
	}{
		"client's":               {client, file, SHA1, wrong},
		"none sent":              {client, file, MD5, ""},
		"server-generated":       {server, file, SHA1, c.SHA1},
		"metadata":               {client, types.Item{Name: "maven-metadata.xml", Actual: c}, SHA1, c.SHA1},
		"metadata before stored": {client, types.Item{Name: "g/a/maven-metadata.xml", Actual: c}, MD5, c.MD5},
		"remote cache":           {remote, types.Item{Name: "app.jar", Cached: true, Actual: c}, SHA1, c.SHA1},
	} {
		if got := ServedChecksum(tc.c, tc.it, tc.a); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// TestDeployOriginals: what each write records as the client's checksums,
// which a sidecar serves under client-checksums.
func TestDeployOriginals(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := sums("payload")
	// A deploy that declares nothing still has its SHA-256 as the client's.
	it, err := w.deploy("plain.bin", "payload", types.Checksums{})
	if err != nil || it.Original != (types.Checksums{SHA256: c.SHA256}) {
		t.Fatalf("plain: %+v %v", it, err)
	}
	if ServedChecksum(w.loc, it, SHA1) != "" || ServedChecksum(w.loc, it, SHA256) != c.SHA256 {
		t.Fatalf("plain sidecars: %+v", it.Original)
	}
	// A declared SHA-256 is kept as sent, even one the server-generated
	// policy lets differ.
	w.loc.ChecksumPolicyType = repo.ServerGeneratedChecksums
	wrong := strings.Repeat("e", 64)
	it, err = w.deploy("declared.bin", "payload", types.Checksums{SHA256: wrong, MD5: c.MD5})
	if err != nil || it.Original != (types.Checksums{SHA256: wrong, MD5: c.MD5}) {
		t.Fatalf("declared: %+v %v", it, err)
	}
	// A checksum deploy that names only the SHA-256 records them all.
	w.loc.ChecksumPolicyType = repo.ClientChecksums
	it, err = w.svc.DeployByChecksum(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "copy.bin"}, Declared: types.Checksums{SHA256: c.SHA256},
	})
	if err != nil || it.Original != c {
		t.Fatalf("checksum deploy: %+v %v", it, err)
	}
	stored, err := w.db.Items().Get(ctx, "libs", false, "copy.bin")
	if err != nil || stored.Original != c || ServedChecksum(w.loc, stored, MD5) != c.MD5 {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	// A file the server writes itself has its computed checksums as the
	// client's, whatever was declared.
	it, err = w.svc.Deploy(ctx, Deploy{
		Target: Target{Repo: w.loc, Path: "written.bin"}, Body: strings.NewReader("payload"),
		Declared: types.Checksums{SHA256: c.SHA256}, Trusted: true,
	})
	if err != nil || it.Original != c || ServedChecksum(w.loc, it, SHA1) != c.SHA1 {
		t.Fatalf("server written: %+v %v", it, err)
	}
	// Fixing a missing checksum by sending it makes the sidecar serve it.
	plain := Target{Repo: w.loc, Path: "plain.bin"}
	if _, err := w.svc.RecordChecksum(ctx, plain, SHA1, c.SHA1); err != nil {
		t.Fatal(err)
	}
	fixed, err := w.db.Items().Get(ctx, "libs", false, "plain.bin")
	if err != nil || ServedChecksum(w.loc, fixed, SHA1) != c.SHA1 {
		t.Fatalf("fixed: %+v %v", fixed, err)
	}
}

func TestDeletePrunesPackageIndex(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	it, err := w.deploy("pkg/1.0/file.tgz", "tgz", types.Checksums{})
	if err != nil {
		t.Fatal(err)
	}
	v := pkgindex.Version{
		RepoKey:     "libs",
		PackageType: repo.Generic,
		Package:     "pkg",
		Version:     "1.0",
	}
	if err := pkgindex.Link(ctx, w.db, v, it.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Delete(ctx, Target{Repo: w.loc, Path: "pkg"}, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Packages().Find(ctx, "libs", "pkg"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the package outlived its only file: %v", err)
	}
}

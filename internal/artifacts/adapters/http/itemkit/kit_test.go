package itemkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// errFault is a failure no mapping knows.
var errFault = errors.New("fault")

// TestUnitGetItemTriesOverlaysInOrder: GetItem hands each overlay the kit,
// the repository the key names and the parsed request, in the order Deps
// lists them, and stops at the first that answers, before the generic
// lookup. When none answers, the generic lookup serves the file.
func TestUnitGetItemTriesOverlaysInOrder(t *testing.T) {
	w := kittest.New(t, inmem.New())
	w.Put(t, w.Local, false, "a/b.txt", []byte("generic"))
	var k *itemkit.Kit
	var asked []string
	overlay := func(name string, answers bool) itemkit.Overlay {
		return func(
			got *itemkit.Kit,
			rw http.ResponseWriter,
			_ *http.Request,
			_ itemkit.Store,
			c repo.Config,
			_ bool,
			req itemkit.ItemRequest,
			_ kernel.Principal,
			_ itemkit.Access,
		) bool {
			if got != k {
				t.Errorf("overlay %s was handed another kit", name)
			}
			asked = append(asked, name+" "+c.Key+"/"+req.Path)
			if !answers {
				return false
			}
			rw.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(rw, name)
			return true
		}
	}
	get := func() *httptest.ResponseRecorder {
		t.Helper()
		asked = nil
		rec := httptest.NewRecorder()
		k.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/libs-local/a/b.txt"))
		return rec
	}

	k = w.Kit(itemkit.Deps{Overlays: []itemkit.Overlay{
		overlay("first", false), overlay("second", true), overlay("third", true),
	}})
	rec := get()
	if got := strings.Join(
		asked,
		", ",
	); got != "first libs-local/a/b.txt, second libs-local/a/b.txt" ||
		rec.Code != http.StatusTeapot ||
		rec.Body.String() != "second" {
		t.Errorf(
			"asked %q and answered %d %q; want the second overlay's answer",
			got,
			rec.Code,
			rec.Body,
		)
	}

	k = w.Kit(itemkit.Deps{Overlays: []itemkit.Overlay{
		overlay("first", false), overlay("second", false),
	}})
	rec = get()
	if got := strings.Join(
		asked,
		", ",
	); got != "first libs-local/a/b.txt, second libs-local/a/b.txt" ||
		rec.Code != http.StatusOK ||
		rec.Body.String() != "generic" {
		t.Errorf("asked %q and answered %d %q; want the file served", got, rec.Code, rec.Body)
	}
}

// TestUnitNewDefaultsPrivateToNone: a kit built without a private-path
// predicate reports no path private, and one built with a predicate keeps it.
func TestUnitNewDefaultsPrivateToNone(t *testing.T) {
	c := repo.Config{Key: kittest.LocalKey, Class: repo.Local}
	if itemkit.New(itemkit.Deps{}).Private(c, ".private/a.txt") {
		t.Error("a kit without a predicate reports a private path")
	}
	k := itemkit.New(itemkit.Deps{Private: func(_ repo.Config, p string) bool {
		return strings.HasPrefix(p, ".private/")
	}})
	if !k.Private(c, ".private/a.txt") || k.Private(c, "public/a.txt") {
		t.Error("the kit does not answer with the predicate it was given")
	}
}

// TestUnitContentWithoutStoreIsNotImplemented: a kit without a store answers
// like an unimplemented operation, and one with a store returns it.
func TestUnitContentWithoutStoreIsNotImplemented(t *testing.T) {
	if _, err := itemkit.New(itemkit.Deps{}).
		Content(); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Errorf("without a store: %v; want ErrNotImplemented", err)
	}
	w := kittest.New(t, inmem.New())
	if st, err := w.Kit(itemkit.Deps{}).Content(); err != nil || st != w.Store {
		t.Errorf("with a store: %v, %v; want the store", st, err)
	}
}

// TestUnitItemErrorStatuses: ItemError answers each error of the item
// service with its status and text, an overwrite refusal with a challenge
// only to an anonymous caller, and anything else with a 500 that names
// nothing internal.
func TestUnitItemErrorStatuses(t *testing.T) {
	k := itemkit.New(itemkit.Deps{Logger: slog.New(slog.DiscardHandler)})
	anonymous := t.Context()
	user := accessapi.WithCaller(
		t.Context(),
		kernel.Principal{Name: "ann"},
		&accessapitest.Decider{},
	)
	denied := &item.OverwriteDeniedError{Repo: kittest.LocalKey, Path: "a.txt", User: "ann"}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		err     error
		status  int
		message string
	}{
		{"api error", anonymous, apierror.New(http.StatusGone, "gone"), http.StatusGone, "gone"},
		{
			"format rejection",
			anonymous,
			fmt.Errorf("deploy: %w", format.Rejectf(http.StatusForbidden, "refused %s", "a.txt")),
			http.StatusForbidden,
			"refused a.txt",
		},
		{"overwrite by a user", user, denied, http.StatusForbidden, denied.Error()},
		{"overwrite by anonymous", anonymous, denied, http.StatusUnauthorized, denied.Error()},
		{
			"sidecar of a missing file",
			anonymous,
			&item.SidecarError{Err: kernel.ErrNotFound, Message: "no file"},
			http.StatusNotFound,
			"no file",
		},
		{
			"refused sidecar",
			anonymous,
			&item.SidecarError{Err: errFault, Message: "refused"},
			http.StatusConflict,
			"refused",
		},
		{
			"checksum mismatch",
			anonymous,
			item.ErrChecksumMismatch,
			http.StatusConflict,
			"Checksum policy rejected the upload: the content does not match the declared checksum",
		},
		{"bad checksum", anonymous, item.ErrBadChecksum, http.StatusBadRequest, item.ErrBadChecksum.Error()},
		{
			"no blob",
			anonymous,
			item.ErrNoBlob,
			http.StatusNotFound,
			"Checksum deploy failed: no stored content matches the checksum",
		},
		{"conflict", anonymous, item.ErrConflict, http.StatusConflict, item.ErrConflict.Error()},
		{"collected", anonymous, item.ErrCollected, http.StatusServiceUnavailable, item.ErrCollected.Error()},
		{"bad archive", anonymous, item.ErrBadArchive, http.StatusBadRequest, item.ErrBadArchive.Error()},
		{
			"invalid path",
			anonymous,
			kernel.ErrInvalidItemPath,
			http.StatusBadRequest,
			kernel.ErrInvalidItemPath.Error(),
		},
		{
			"long property key",
			anonymous,
			fmt.Errorf("set: %w", model.ErrPropertyKeyTooLong),
			http.StatusBadRequest,
			model.ErrPropertyKeyTooLong.Error(),
		},
		{"not found", anonymous, kernel.ErrNotFound, http.StatusNotFound, "Could not find resource"},
		{"anything else", anonymous, errFault, http.StatusInternalServerError, "internal error"},
	} {
		var got *apierror.Error
		if err := k.ItemError(tc.ctx, "op", tc.err); !errors.As(err, &got) ||
			got.Status != tc.status || got.Message != tc.message {
			t.Errorf("%s: %v; want %d %q", tc.name, err, tc.status, tc.message)
		}
	}
}

// TestUnitLookupSearchesVirtualMembersInOrder: a virtual repository's lookup
// tries its members in order, finds a remote member's file in its cache,
// passes over a member the caller may not read, and never searches a
// repository twice, which ends a virtual repository that includes itself.
func TestUnitLookupSearchesVirtualMembersInOrder(t *testing.T) {
	w := kittest.New(t, inmem.New())
	ctx := t.Context()
	w.Put(t, w.Local, false, "a.txt", []byte("local"))
	w.Put(t, w.Remote, true, "a.txt", []byte("cached"))
	w.Put(t, w.Remote, true, "b.txt", []byte("cached"))
	admin := kittest.AdminAccess()
	remoteOnly := itemkit.Access{Decider: &accessapitest.Decider{
		AllowsFolderFunc: func(
			_ accessapi.ResourceType,
			r accessapi.Repo,
			_ string,
			_ accessapi.Actions,
		) bool {
			return r.Key == kittest.RemoteKey
		},
	}}
	for _, tc := range []struct {
		path   string
		acc    itemkit.Access
		repo   string
		cached bool
	}{
		{"a.txt", admin, kittest.LocalKey, false},
		{"b.txt", admin, kittest.RemoteKey, true},
		{"a.txt", remoteOnly, kittest.RemoteKey, true},
	} {
		f, err := itemkit.Lookup(
			ctx,
			w.Store,
			w.Virtual,
			false,
			tc.path,
			tc.acc,
			nil,
			map[string]bool{},
		)
		if err != nil || f.Repo.Key != tc.repo || f.Item.RepoKey != tc.repo ||
			f.Item.Cached != tc.cached || f.Item.FullPath() != tc.path {
			t.Errorf("%s: %s %+v, %v; want %s", tc.path, f.Repo.Key, f.Item, err, tc.repo)
		}
	}
	if _, err := itemkit.Lookup(
		ctx, w.Store, w.Virtual, false, "c.txt", admin, nil, map[string]bool{},
	); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a file no member holds: %v; want ErrNotFound", err)
	}
	if _, err := itemkit.Lookup(
		ctx,
		w.Store,
		w.Virtual,
		false,
		"a.txt",
		admin,
		nil,
		map[string]bool{kittest.VirtualKey: true},
	); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a repository searched already: %v; want ErrNotFound", err)
	}
}

// TestUnitResolveRepoNamesOnlyRemoteCaches: a key resolves to the repository
// it names, and a cache alias only to a remote repository's cache.
func TestUnitResolveRepoNamesOnlyRemoteCaches(t *testing.T) {
	w := kittest.New(t, inmem.New())
	for _, tc := range []struct {
		key    string
		repo   string
		cached bool
		status int
	}{
		{kittest.LocalKey, kittest.LocalKey, false, 0},
		{kittest.RemoteKey + "-cache", kittest.RemoteKey, true, 0},
		{kittest.LocalKey + "-cache", "", true, http.StatusNotFound},
		{"missing", "", false, http.StatusNotFound},
	} {
		c, cached, err := itemkit.ResolveRepo(t.Context(), w.Store, tc.key)
		var api *apierror.Error
		switch {
		case tc.status == 0 && (err != nil || c.Key != tc.repo || cached != tc.cached):
			t.Errorf("%s: %s, %t, %v; want %s, %t", tc.key, c.Key, cached, err, tc.repo, tc.cached)
		case tc.status != 0 && (!errors.As(err, &api) || api.Status != tc.status ||
			api.Message != "Repository '"+tc.key+"' does not exist"):
			t.Errorf("%s: %v; want %d", tc.key, err, tc.status)
		}
	}
}

// TestUnitDeployTargetResolvesVirtualRepositories: a deploy lands in a
// hosted repository itself or in a virtual repository's default deployment
// repository, and is refused by a remote repository, its cache, a virtual
// repository without a default deployment repository and P2.
func TestUnitDeployTargetResolvesVirtualRepositories(t *testing.T) {
	w := kittest.New(t, inmem.New())
	for _, tc := range []struct {
		name   string
		c      repo.Config
		cached bool
		target string
	}{
		{"local", w.Local, false, kittest.LocalKey},
		{"federated", w.Federated, false, kittest.FederatedKey},
		{"virtual", w.Virtual, false, kittest.LocalKey},
		{"remote", w.Remote, false, ""},
		{"cache", w.Remote, true, ""},
		{"virtual without a target", repo.Config{Key: "v", Class: repo.Virtual}, false, ""},
		{"p2", repo.Config{Key: "p2", Class: repo.Virtual, PackageType: repo.P2}, false, ""},
	} {
		got, err := itemkit.DeployTarget(t.Context(), w.Store, tc.c, tc.cached)
		var api *apierror.Error
		switch {
		case tc.target != "" && (err != nil || got.Key != tc.target):
			t.Errorf("%s: %s, %v; want %s", tc.name, got.Key, err, tc.target)
		case tc.target == "" && (!errors.As(err, &api) || api.Status != http.StatusMethodNotAllowed):
			t.Errorf("%s: %v; want 405", tc.name, err)
		}
	}
}

// TestUnitGetItemAndTraceRefuseWhatTheyCannotServe: without a caller a
// download and its trace answer 401; without a store the trace answers 501,
// and a path that is not an item path 400; a file whose content the blob
// store no longer holds answers 500, an entry in such an archive answers
// with the archive's own 500, and the entry's trace ends on that 500.
func TestUnitGetItemAndTraceRefuseWhatTheyCannotServe(t *testing.T) {
	w := kittest.New(t, inmem.New())
	k := w.Kit(itemkit.Deps{Logger: slog.New(slog.DiscardHandler)})
	for _, name := range []string{"lost.txt", "lost.zip"} {
		it := w.Put(t, w.Local, false, name, []byte("content of "+name))
		if err := w.Blobs.Delete(t.Context(), it.Actual.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	anonymous := func(target string) *http.Request {
		return httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	}
	for _, tc := range []struct {
		name   string
		k      *itemkit.Kit
		r      *http.Request
		status int
	}{
		{"download without a caller", k, anonymous("/artifactory/libs-local/a.txt"), 401},
		{"trace without a caller", k, anonymous("/artifactory/libs-local/a.txt?trace"), 401},
		{
			"trace without a store",
			itemkit.New(itemkit.Deps{}),
			kittest.Request(t, http.MethodGet, "/artifactory/libs-local/a.txt?trace"),
			http.StatusNotImplemented,
		},
		{
			"trace of a path with a dot-dot segment",
			k,
			kittest.Request(t, http.MethodGet, "/artifactory/libs-local/a/../b.txt?trace"),
			http.StatusBadRequest,
		},
		{
			"file without its content",
			k,
			kittest.Request(t, http.MethodGet, "/artifactory/libs-local/lost.txt"),
			http.StatusInternalServerError,
		},
	} {
		rec := httptest.NewRecorder()
		tc.k.GetItem(rec, tc.r)
		if rec.Code != tc.status {
			t.Errorf("%s: %d %s; want %d", tc.name, rec.Code, rec.Body, tc.status)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		archive, entry := httptest.NewRecorder(), httptest.NewRecorder()
		k.GetItem(archive, kittest.Request(t, method, "/artifactory/libs-local/lost.zip"))
		k.GetItem(entry, kittest.Request(t, method, "/artifactory/libs-local/lost.zip!/a.txt"))
		if archive.Code != http.StatusInternalServerError || entry.Code != archive.Code ||
			entry.Header().Get("Content-Type") != archive.Header().Get("Content-Type") ||
			entry.Body.String() != archive.Body.String() {
			t.Errorf("%s of an entry in a lost archive: %d %q; the archive's: %d %q",
				method, entry.Code, entry.Body, archive.Code, archive.Body)
		}
	}
	rec := httptest.NewRecorder()
	k.GetItem(
		rec,
		kittest.Request(t, http.MethodGet, "/artifactory/libs-local/lost.zip!/a.txt?trace"),
	)
	if rec.Code != http.StatusOK || !strings.HasSuffix(rec.Body.String(),
		" Sending a response with the status '500' and the message 'internal error'\n") {
		t.Errorf("trace of an entry in a lost archive: %d %q", rec.Code, rec.Body)
	}
}

// TestUnitArchiveEntryFailureAfterItsStatusIsLogged: a zip entry whose CRC-32
// is wrong fails only at its end, after its 200 and its bytes are sent, so
// the failure is logged and nothing is written after the entry.
func TestUnitArchiveEntryFailureAfterItsStatusIsLogged(t *testing.T) {
	const content = "inside"
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	fw, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "a.txt",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE([]byte(content)) ^ 1,
		CompressedSize64:   uint64(len(content)),
		UncompressedSize64: uint64(len(content)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	w := kittest.New(t, inmem.New())
	w.Put(t, w.Local, false, "bad.zip", archive.Bytes())
	var logs bytes.Buffer
	k := w.Kit(itemkit.Deps{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	rec := httptest.NewRecorder()
	k.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/libs-local/bad.zip!/a.txt"))
	if rec.Code != http.StatusOK || rec.Body.String() != content ||
		!strings.Contains(logs.String(), zip.ErrChecksum.Error()) {
		t.Errorf("entry failing its checksum: %d %q, logged %q", rec.Code, rec.Body, logs.String())
	}
}

// TestUnitStatusWriterCountsWhatItWrites: a body written before any status
// counts as a 200, through Write or ReadFrom; it is complete when no length
// was declared or when the declared length was written; and Unwrap reaches
// the writer underneath.
func TestUnitStatusWriterCountsWhatItWrites(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "/artifactory/libs-local/a.txt", nil)
	written := httptest.NewRecorder()
	byWrite := &itemkit.StatusWriter{ResponseWriter: written}
	if _, err := byWrite.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if !byWrite.Complete(get) || written.Code != http.StatusOK || written.Body.String() != "abc" ||
		byWrite.Unwrap() != written {
		t.Errorf("Write: complete %t, %d %q", byWrite.Complete(get), written.Code, written.Body)
	}
	copied := httptest.NewRecorder()
	copied.Header().Set("Content-Length", "4")
	byCopy := &itemkit.StatusWriter{ResponseWriter: copied}
	if _, err := byCopy.ReadFrom(strings.NewReader("abc")); err != nil {
		t.Fatal(err)
	}
	if byCopy.Complete(get) || copied.Code != http.StatusOK {
		t.Errorf("ReadFrom short of its length: complete %t, %d", byCopy.Complete(get), copied.Code)
	}
	if _, err := byCopy.ReadFrom(strings.NewReader("d")); err != nil {
		t.Fatal(err)
	}
	if !byCopy.Complete(get) || copied.Body.String() != "abcd" {
		t.Errorf("ReadFrom of its length: complete %t, %q", byCopy.Complete(get), copied.Body)
	}
}

// TestUnitDebianSourceRequestValidatesRemoteMetadata: a Release file, a
// by-hash index and a Packages or Sources index are requested as metadata
// whose validator refuses, as a 502 integrity failure, an empty Release file
// or by-hash index, an index of unknown size, a body short of its size, a
// Release file that does not parse and a by-hash file that does not match
// its hash, and accepts an empty Packages or Sources index and one that does
// not parse, which a remote caches as fetched (#1356); a query on a Release
// file is kept for the source boundary (#1349), and a by-hash or index path
// apt never asks for is not found.
func TestUnitDebianSourceRequestValidatesRemoteMetadata(t *testing.T) {
	request := func(p, query string) (source.Request, error) {
		return itemkit.DebianSourceRequest(source.Request{
			Resource: source.Resource{Path: p, Query: query},
		})
	}
	if q, err := request("dists/stable/Release", "x=1"); err != nil || q.Resource.Query != "x=1" {
		t.Errorf("query on a Release file: %q, %v; want the query kept", q.Resource.Query, err)
	}
	for _, p := range []string{
		"dists/stable/main/binary-amd64/by-hash/SHA256/abc",
		"dists/stable/Packages",
	} {
		if _, err := request(p, ""); !errors.Is(err, kernel.ErrNotFound) {
			t.Errorf("%s: %v; want ErrNotFound", p, err)
		}
	}
	const armor = "-----BEGIN PGP SIGNATURE-----\nabc\n-----END PGP SIGNATURE-----\n"
	const release, packages = "dists/stable/Release.gpg", "dists/stable/main/binary-amd64/Packages"
	sum := sha256.Sum256([]byte("index"))
	byHash := "dists/stable/main/binary-amd64/by-hash/SHA256/" + hex.EncodeToString(sum[:])
	for _, tc := range []struct {
		name, path, variant, body string
		size                      int64
		valid                     bool
	}{
		{"empty signature", release, "debian-release-v1", "", 0, false},
		{"signature short of its size", release, "debian-release-v1", armor, int64(len(armor)) + 1, false},
		{"unarmored signature", release, "debian-release-v1", "signature", 9, false},
		{"signature", release, "debian-release-v1", armor, int64(len(armor)), true},
		{"empty by-hash index", byHash, "debian-by-hash-v1", "", 0, false},
		{"by-hash index of other content", byHash, "debian-by-hash-v1", "other", 5, false},
		{"by-hash index", byHash, "debian-by-hash-v1", "index", 5, true},
		{"Packages of unknown size", packages, "debian-packages-v2", "", -1, false},
		{"Packages short of its size", packages, "debian-packages-v2", "x", 2, false},
		{"unterminated Packages stanza", packages, "debian-packages-v2", "Package: a", 10, true},
		{"empty Packages", packages, "debian-packages-v2", "", 0, true},
		{"empty Sources", "dists/stable/main/source/Sources", "debian-sources-v2", "", 0, true},
	} {
		q, err := request(tc.path, "")
		if err != nil || q.Resource.Kind != source.ResourceKindMetadata ||
			q.Resource.Variant != tc.variant || q.Resource.ValidateMetadata == nil {
			t.Errorf("%s: %+v, %v", tc.name, q.Resource, err)
			continue
		}
		err = q.Resource.ValidateMetadata(t.Context(), strings.NewReader(tc.body), tc.size,
			source.MetadataResponse{})
		var failure *source.HTTPError
		switch {
		case tc.valid && err != nil:
			t.Errorf("%s: %v; want valid", tc.name, err)
		case !tc.valid && (!errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
			failure.Kind != source.FailureKindIntegrity):
			t.Errorf("%s: %v; want a 502 integrity failure", tc.name, err)
		}
	}
}

// TestUnitSourceRequestsCheckAlpineAndPassOtherPaths: a query on an Alpine
// package is refused, and the APKINDEX and package validators refuse, as a
// 502 integrity failure, a body of no or the wrong size and one that does not
// parse; CRAN and RPM pass a path they do not validate through unchanged.
func TestUnitSourceRequestsCheckAlpineAndPassOtherPaths(t *testing.T) {
	const pkg, index = "v3.20/main/x86_64/hello-1.0-r0.apk", "v3.20/main/x86_64/APKINDEX.tar.gz"
	alpineRequest := func(p, query string) (source.Request, error) {
		return itemkit.AlpineSourceRequest(source.Request{
			Resource: source.Resource{Path: p, Query: query},
		})
	}
	if _, err := alpineRequest(pkg, "x=1"); !errors.Is(err, source.ErrInvalidRequest) {
		t.Errorf("query on an Alpine package: %v; want ErrInvalidRequest", err)
	}
	integrity := func(name string, err error) {
		t.Helper()
		var failure *source.HTTPError
		if !errors.As(err, &failure) || failure.Status != http.StatusBadGateway ||
			failure.Kind != source.FailureKindIntegrity {
			t.Errorf("%s: %v; want a 502 integrity failure", name, err)
		}
	}
	q, err := alpineRequest(index, "")
	if err != nil || q.Resource.Kind != source.ResourceKindMetadata ||
		q.Resource.Variant != "alpine-index-v2" || q.Resource.ValidateMetadata == nil {
		t.Fatalf("APKINDEX: %+v, %v", q.Resource, err)
	}
	for _, tc := range []struct {
		name, body string
		size       int64
	}{
		{"empty APKINDEX", "", 0},
		{"APKINDEX short of its size", "x", 2},
		{"APKINDEX that is not an archive", "index", 5},
	} {
		integrity(tc.name, q.Resource.ValidateMetadata(t.Context(), strings.NewReader(tc.body),
			tc.size, source.MetadataResponse{}))
	}
	q, err = alpineRequest(pkg, "")
	if err != nil || q.Resource.Variant != "alpine-package-v2" || q.Resource.Validate == nil {
		t.Fatalf("Alpine package: %+v, %v", q.Resource, err)
	}
	integrity("package that is not an APK", q.Resource.Validate(t.Context(),
		strings.NewReader("package"), 7))
	for name, sourceRequest := range map[string]func(source.Request) (source.Request, error){
		"CRAN": itemkit.CranSourceRequest, "RPM": itemkit.RPMSourceRequest,
	} {
		in := source.Request{Resource: source.Resource{Path: "docs/README.txt", Query: "x=1"}}
		if out, err := sourceRequest(in); err != nil || out.Resource.Path != in.Resource.Path ||
			out.Resource.Query != in.Resource.Query || out.Resource.Variant != "" {
			t.Errorf("%s pass-through: %+v, %v", name, out.Resource, err)
		}
	}
}

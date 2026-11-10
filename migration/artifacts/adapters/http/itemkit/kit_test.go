package itemkit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// errFault is a failure no mapping knows.
var errFault = errors.New("fault")

// TestUnitGetItemTriesOverlaysInOrder: GetItem hands each overlay the kit,
// the repository the key names and the parsed request, in the order Deps
// lists them, and stops at the first that answers, before the generic
// lookup. When none answers, the generic lookup serves the file.
func TestUnitGetItemTriesOverlaysInOrder(t *testing.T) {
	w := kittest.New(t)
	w.Put(t, w.Local, false, "a/b.txt", []byte("generic"))
	var k *itemkit.Kit
	var asked []string
	overlay := func(name string, answers bool) itemkit.Overlay {
		return func(
			got *itemkit.Kit,
			rw http.ResponseWriter,
			_ *http.Request,
			_ store.Store,
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
	w := kittest.New(t)
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
			&item.SidecarError{Err: store.ErrNotFound, Message: "no file"},
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
			types.ErrInvalidItemPath,
			http.StatusBadRequest,
			types.ErrInvalidItemPath.Error(),
		},
		{
			"long property key",
			anonymous,
			fmt.Errorf("set: %w", types.ErrPropertyKeyTooLong),
			http.StatusBadRequest,
			types.ErrPropertyKeyTooLong.Error(),
		},
		{"not found", anonymous, store.ErrNotFound, http.StatusNotFound, "Could not find resource"},
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
	w := kittest.New(t)
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
	); !errors.Is(err, store.ErrNotFound) {
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
	); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a repository searched already: %v; want ErrNotFound", err)
	}
}

// TestUnitResolveRepoNamesOnlyRemoteCaches: a key resolves to the repository
// it names, and a cache alias only to a remote repository's cache.
func TestUnitResolveRepoNamesOnlyRemoteCaches(t *testing.T) {
	w := kittest.New(t)
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
	w := kittest.New(t)
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

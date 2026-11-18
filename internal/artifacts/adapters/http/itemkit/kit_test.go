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
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
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
		// Only a deploy whose content was collected beside it reports a
		// transient condition, which a retry clears (#1415).
		if retry := tc.err == item.ErrCollected; retry != (got.RetryAfterSeconds == 1) ||
			!retry && got.RetryAfterSeconds != 0 {
			t.Errorf("%s: RetryAfterSeconds %d", tc.name, got.RetryAfterSeconds)
		}
	}
}

var errRepos = errors.New("repositories unavailable")

// failingRepos is a store whose repository reads fail.
type failingRepos struct{ *inmem.Store }

func (s failingRepos) Repos() port.RepoStore { return failedRepoReads{s.Store.Repos()} }

type failedRepoReads struct{ port.RepoStore }

func (failedRepoReads) Get(context.Context, string) (repo.Config, error) {
	return repo.Config{}, errRepos
}

// TestUnitLookupSearchesVirtualMembersInOrder: a virtual repository's lookup
// tries its members in 7.x's order, local members before remote members'
// caches whatever the configured order (decision 0008, R1), finds a remote
// member's file in its cache, passes over a member the caller may not read,
// and skips a virtual repository it has reached already, such as itself.
func TestUnitLookupSearchesVirtualMembersInOrder(t *testing.T) {
	w := kittest.New(t, inmem.New())
	ctx := t.Context()
	w.Put(t, w.Local, false, "a.txt", []byte("local"))
	w.Put(t, w.Remote, true, "a.txt", []byte("cached"))
	w.Put(t, w.Remote, true, "b.txt", []byte("cached"))
	remoteFirst := repo.Config{
		Key:          "remote-first",
		Class:        repo.Virtual,
		Repositories: []string{"remote-first", kittest.RemoteKey, kittest.LocalKey},
	}
	if err := w.Store.Repos().Create(ctx, *repo.Defaults(&remoteFirst)); err != nil {
		t.Fatal(err)
	}
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
		virtual repo.Config
		path    string
		acc     itemkit.Access
		repo    string
		cached  bool
	}{
		{w.Virtual, "a.txt", admin, kittest.LocalKey, false},
		{w.Virtual, "b.txt", admin, kittest.RemoteKey, true},
		{w.Virtual, "a.txt", remoteOnly, kittest.RemoteKey, true},
		{remoteFirst, "a.txt", admin, kittest.LocalKey, false},
		{remoteFirst, "b.txt", admin, kittest.RemoteKey, true},
	} {
		f, err := itemkit.Lookup(
			ctx,
			w.Store,
			tc.virtual,
			false,
			tc.path,
			tc.acc,
			nil,
			virtual.Resolve,
		)
		if err != nil || f.Repo.Key != tc.repo || f.Item.RepoKey != tc.repo ||
			f.Item.Cached != tc.cached || f.Item.FullPath() != tc.path {
			t.Errorf("%s/%s: %s %+v, %v; want %s",
				tc.virtual.Key, tc.path, f.Repo.Key, f.Item, err, tc.repo)
		}
	}
	if _, err := itemkit.Lookup(
		ctx, w.Store, w.Virtual, false, "c.txt", admin, nil, virtual.Resolve,
	); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("a file no member holds: %v; want ErrNotFound", err)
	}
	// A member the store cannot read fails the lookup.
	if _, err := itemkit.Lookup(
		ctx, failingRepos{w.Store}, w.Virtual, false, "a.txt", admin, nil, virtual.Resolve,
	); !errors.Is(err, errRepos) {
		t.Errorf("a failed member read: %v; want errRepos", err)
	}
}

// failingMembers is a store whose reads of every repository but the
// virtual one fail, so that a virtual repository resolves and its members
// do not.
type failingMembers struct{ *inmem.Store }

func (s failingMembers) Repos() port.RepoStore { return failedMemberReads{s.Store.Repos()} }

type failedMemberReads struct{ port.RepoStore }

func (r failedMemberReads) Get(ctx context.Context, key string) (repo.Config, error) {
	if key != kittest.VirtualKey {
		return repo.Config{}, errRepos
	}
	return r.RepoStore.Get(ctx, key) //nolint:wrapcheck // the store's own answer
}

// failingItems is a store whose read of broken.txt and of any item's
// properties fail.
type failingItems struct{ *inmem.Store }

func (s failingItems) Items() port.ItemStore { return failedItemReads{s.Store.Items()} }

type failedItemReads struct{ port.ItemStore }

func (r failedItemReads) Get(
	ctx context.Context,
	key string,
	cached bool,
	fullPath string,
) (model.Item, error) {
	if fullPath == "broken.txt" {
		return model.Item{}, errFault
	}
	return r.ItemStore.Get(ctx, key, cached, fullPath) //nolint:wrapcheck // the store's own answer
}

func (failedItemReads) Properties(context.Context, int64) (model.Properties, error) {
	return nil, errFault
}

// localRefusing is a format that refuses every download from the local
// member, as Maven's release and snapshot policy refuses a path.
type localRefusing struct{ format.Handler }

func (localRefusing) Resolve(
	_ context.Context,
	_ format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if c.Key == kittest.LocalKey {
		return "", format.Rejectf(http.StatusConflict, "refused by %s", c.Key)
	}
	return d.Path, nil
}

// TestUnitVirtualReadGateAndWalkFailures: Admits asks Read of a real
// repository itself, never of a virtual repository's path, and of a
// virtual repository's root asks its members, an administrator passing
// without one (#1533). A walk passes over a member whose format refuses the
// path, as 7.x's processStandard passes over any refusal that is not a
// 403; a member, item or property read that fails answers 500, at the
// root's gate and in the walk alike.
func TestUnitVirtualReadGateAndWalkFailures(t *testing.T) {
	w := kittest.New(t, inmem.New())
	ctx := t.Context()
	w.Put(t, w.Local, false, "a.txt", []byte("local"))
	w.Put(t, w.Remote, true, "a.txt", []byte("cached"))
	w.Put(t, w.Local, false, "broken.txt", []byte("local"))
	remoteOnly := itemkit.Access{Decider: &accessapitest.Decider{AllowsFolderFunc: func(
		_ accessapi.ResourceType, r accessapi.Repo, _ string, _ accessapi.Actions,
	) bool {
		return r.Key == kittest.RemoteKey
	}}}
	nothing := itemkit.Access{Decider: &accessapitest.Decider{}}
	for _, tc := range []struct {
		name string
		acc  itemkit.Access
		c    repo.Config
		path string
		want bool
	}{
		{"local, readable", remoteOnly, w.Remote, "a.txt", true},
		{"local, unreadable", remoteOnly, w.Local, "a.txt", false},
		{"virtual path", nothing, w.Virtual, "a.txt", true},
		{"virtual root, a member readable", remoteOnly, w.Virtual, "", true},
		{"virtual root, none readable", nothing, w.Virtual, "", false},
		{"virtual root, admin", kittest.AdminAccess(), w.Virtual, "", true},
	} {
		got, err := tc.acc.Admits(ctx, w.Store.Repos(), tc.c, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v, want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := remoteOnly.Admits(
		ctx, failedMemberReads{w.Store.Repos()}, w.Virtual, "",
	); !errors.Is(err, errRepos) {
		t.Errorf("a failed member read: %v; want errRepos", err)
	}
	user := &accessapitest.Decider{AllowsFolderFunc: func(
		accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
	) bool {
		return true
	}}
	for _, tc := range []struct {
		name, target string
		kit          *itemkit.Kit
		decider      accessapi.Decider
		status       int
		// body is a part of the answer's body.
		body string
	}{
		{
			"a member's format refusal", "libs/a.txt",
			w.Kit(itemkit.Deps{Formats: func(repo.PackageType) format.Handler {
				return localRefusing{}
			}}),
			kittest.AdminAccess().Decider, http.StatusOK, "cached",
		},
		{
			"the root's gate", "libs/", w.Kit(itemkit.Deps{Store: failingMembers{w.Store}}),
			user, http.StatusInternalServerError, "",
		},
		{
			// A trace answers 200 with the status inside.
			"the root's gate, traced", "libs/?trace",
			w.Kit(itemkit.Deps{Store: failingMembers{w.Store}}),
			user, http.StatusOK, "Sending a response with the status '500'",
		},
		{
			"an item read", "libs/broken.txt", w.Kit(itemkit.Deps{Store: failingItems{w.Store}}),
			kittest.AdminAccess().Decider, http.StatusInternalServerError, "",
		},
		{
			"a property read", "libs/a.txt;k=v", w.Kit(itemkit.Deps{Store: failingItems{w.Store}}),
			kittest.AdminAccess().Decider, http.StatusInternalServerError, "",
		},
		{
			// A direct trace reads the properties itself to record the
			// match (traceMatrix), outside the walk.
			"a property read, traced", kittest.LocalKey + "/a.txt;k=v?trace",
			w.Kit(itemkit.Deps{Store: failingItems{w.Store}}),
			kittest.AdminAccess().Decider, http.StatusOK, "Sending a response with the status '500'",
		},
	} {
		r := kittest.Request(t, http.MethodGet, "/artifactory/"+tc.target)
		r = r.WithContext(
			accessapi.WithCaller(r.Context(), kernel.Principal{Name: "user"}, tc.decider),
		)
		rec := httptest.NewRecorder()
		tc.kit.GetItem(rec, r)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s: %d %s, want %d with %q", tc.name, rec.Code, rec.Body, tc.status, tc.body)
		}
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
// hosted repository itself or in a virtual repository's local or federated
// default deployment repository. A virtual repository without one answers
// 7.x's 405 with Allow: GET, whether the default deployment repository is
// unset, missing, remote or virtual (decision 0008, R6). A remote
// repository and its cache answer the native protocols' 405 through
// DeployTarget and 7.x's 404 through ItemDeployTarget, and P2 keeps its own
// 405 through both.
func TestUnitDeployTargetResolvesVirtualRepositories(t *testing.T) {
	w := kittest.New(t, inmem.New())
	via := func(ddr string) repo.Config {
		v := w.Virtual
		v.Key, v.DefaultDeploymentRepo = "v", ddr
		return v
	}
	const (
		noTarget = "No local repository was configured as local deployment repository " +
			"for the (v) virtual repository."
		p2 = "P2 repositories are remote or virtual; publish to a local Maven or Generic member"
	)
	for _, tc := range []struct {
		name   string
		c      repo.Config
		cached bool
		target string
		status int
		msg    string
		// itemStatus and itemMsg are ItemDeployTarget's, where they differ.
		itemStatus int
		itemMsg    string
	}{
		{name: "local", c: w.Local, target: kittest.LocalKey},
		{name: "federated", c: w.Federated, target: kittest.FederatedKey},
		{name: "virtual", c: w.Virtual, target: kittest.LocalKey},
		{name: "virtual to a federated repository", c: via(kittest.FederatedKey), target: kittest.FederatedKey},
		{name: "virtual without a target", c: via(""), status: 405, msg: noTarget},
		{name: "virtual to a missing repository", c: via("gone"), status: 405, msg: noTarget},
		{name: "virtual to a remote repository", c: via(kittest.RemoteKey), status: 405, msg: noTarget},
		{name: "virtual to a virtual repository", c: via(kittest.VirtualKey), status: 405, msg: noTarget},
		{
			name: "remote", c: w.Remote, status: 405,
			msg:        "Cannot deploy to the remote repository 'libs-remote'",
			itemStatus: 404, itemMsg: "Could not find a local repository named libs-remote to deploy to.",
		},
		{
			name: "cache", c: w.Remote, cached: true, status: 405,
			msg:        "Cannot deploy to the remote repository 'libs-remote'",
			itemStatus: 404, itemMsg: "Could not find a local repository named libs-remote-cache to deploy to.",
		},
		{
			name: "p2", c: repo.Config{Key: "p2", Class: repo.Virtual, PackageType: repo.P2},
			status: 405, msg: p2,
		},
		{
			name: "p2 remote", c: repo.Config{Key: "p2r", Class: repo.Remote, PackageType: repo.P2},
			status: 405, msg: p2,
		},
	} {
		for _, item := range []bool{false, true} {
			deploy, status, msg := itemkit.DeployTarget, tc.status, tc.msg
			if item {
				deploy = itemkit.ItemDeployTarget
				if tc.itemStatus != 0 {
					status, msg = tc.itemStatus, tc.itemMsg
				}
			}
			got, err := deploy(t.Context(), w.Store, tc.c, tc.cached)
			var api *apierror.Error
			allow := ""
			if status == http.StatusMethodNotAllowed && msg == noTarget {
				allow = http.MethodGet
			}
			switch {
			case tc.target != "" && (err != nil || got.Key != tc.target):
				t.Errorf("%s (item %t): %s, %v; want %s", tc.name, item, got.Key, err, tc.target)
			case tc.target == "" && (!errors.As(err, &api) || api.Status != status ||
				api.Message != msg || api.Allow != allow):
				t.Errorf(
					"%s (item %t): %#v; want %d %q, Allow %q",
					tc.name,
					item,
					err,
					status,
					msg,
					allow,
				)
			}
		}
	}
	// A failed read of the default deployment repository is the store's
	// error, which the item routes answer 500.
	if _, err := itemkit.DeployTarget(
		t.Context(), failingRepos{inmem.New()}, w.Virtual, false,
	); !errors.Is(err, errRepos) {
		t.Errorf("a failed repository read: %v", err)
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

// TestUnitArchiveEntryFailureAfterItsStatusIsLogged: an entry is read
// through for its checksums before anything is sent, so a zip entry whose
// CRC-32 is wrong, which fails only at its end, answers 404 as a corrupt
// archive does (#1593). An archive whose blob fails once the entry's 200
// is sent, on the read that serves it, has the failure logged, nothing
// written after it, and no download counted.
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
	k := w.Kit(itemkit.Deps{})
	rec := httptest.NewRecorder()
	k.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/libs-local/bad.zip!/a.txt"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("entry failing its checksum: %d %q", rec.Code, rec.Body)
	}

	sound := zip.NewWriter(&archive)
	archive.Reset()
	fw, err = sound.Create("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatal(err)
	}
	if err := sound.Close(); err != nil {
		t.Fatal(err)
	}
	good := w.Put(t, w.Local, false, "good.zip", archive.Bytes())
	var logs bytes.Buffer
	opens := 0
	k = w.Kit(itemkit.Deps{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		Blobs:  droppedOnSecondOpen{Store: w.Blobs, opens: &opens},
	})
	rec = httptest.NewRecorder()
	k.GetItem(rec, kittest.Request(t, http.MethodGet, "/artifactory/libs-local/good.zip!/a.txt"))
	if rec.Code != http.StatusOK || rec.Body.String() != "" || opens != 2 ||
		!strings.Contains(logs.String(), errDropped.Error()) {
		t.Errorf("entry whose blob fails on the read that serves it: %d %q, %d opens, logged %q",
			rec.Code, rec.Body, opens, logs.String())
	}
	stats, err := w.Store.Items().Stats(t.Context(), good.ID)
	if err != nil || stats.DownloadCount != 0 {
		t.Errorf("a failed entry counted as a download: %d %v", stats.DownloadCount, err)
	}
}

var errDropped = errors.New("the store dropped the connection")

// droppedOnSecondOpen opens a blob soundly once, as an entry's checksums
// read it, and then as content whose every read fails, as a store whose
// connection drops does.
type droppedOnSecondOpen struct {
	blob.Store
	opens *int
}

func (d droppedOnSecondOpen) Open(
	ctx context.Context,
	sha string,
) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := d.Store.Open(ctx, sha)
	*d.opens++
	if err != nil || *d.opens < 2 {
		return r, info, err
	}
	_ = r.Close()
	return droppedContent{}, info, nil
}

type droppedContent struct{}

func (droppedContent) Read([]byte) (int, error)          { return 0, errDropped }
func (droppedContent) ReadAt([]byte, int64) (int, error) { return 0, errDropped }
func (droppedContent) Seek(int64, int) (int64, error)    { return 0, nil }
func (droppedContent) Close() error                      { return nil }

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
// package is kept for the source boundary, which passes it on only under
// Propagate Query Params (#1350), and the APKINDEX and package validators
// refuse, as a
// 502 integrity failure, a body of no or the wrong size and one that does not
// parse; CRAN and RPM pass a path they do not validate through unchanged.
func TestUnitSourceRequestsCheckAlpineAndPassOtherPaths(t *testing.T) {
	const pkg, index = "v3.20/main/x86_64/hello-1.0-r0.apk", "v3.20/main/x86_64/APKINDEX.tar.gz"
	alpineRequest := func(p, query string) (source.Request, error) {
		return itemkit.AlpineSourceRequest(source.Request{
			Resource: source.Resource{Path: p, Query: query},
		})
	}
	for _, p := range []string{pkg, index} {
		if q, err := alpineRequest(p, "x=1"); err != nil || q.Resource.Query != "x=1" {
			t.Errorf("query on Alpine %s: %+v, %v; want it kept", p, q.Resource, err)
		}
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

// TestUnitTraceAsksTheOverlays (#1595): ?trace lets the overlays decide
// first, as GetItem does, and traces what the one that answers would
// answer, as 7.161.26's RepoFilter.doGetOrHeadRequest runs the download
// itself under a TraceLoggingResponse: its error, by the status and the
// message of its envelope, or the status text of any other body, without
// naming the repository to a caller it refuses; or, where it would serve,
// the file it found, through the kit's helpers or a body of its own,
// without serving, redirecting or counting anything. A read it makes
// through GetItem, an archive entry's included, is decided, not traced
// again, and a path every overlay passes keeps the generic trace.
func TestUnitTraceAsksTheOverlays(t *testing.T) {
	w := kittest.New(t, inmem.New())
	file := w.Put(t, w.Local, false, "a/b.txt", []byte("generic"))
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	entry, err := zw.Create("x.txt")
	if err == nil {
		_, err = entry.Write([]byte("x"))
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	archive := w.Put(t, w.Local, false, "z.zip", zipped.Bytes())
	var k *itemkit.Kit
	closed := false
	overlay := func(
		got *itemkit.Kit, rw http.ResponseWriter, r *http.Request, st itemkit.Store, c repo.Config,
		cached bool, req itemkit.ItemRequest, p kernel.Principal, acc itemkit.Access,
	) bool {
		switch req.Path {
		case "miss.idx":
			httpkit.WriteErr(rw, r, itemkit.Missed(c, cached, req))
		case "refused.idx":
			httpkit.WriteErr(rw, r, itemkit.Refused(c, req, p))
		case "plain.idx":
			rw.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(rw, "not an envelope")
		case "big.idx":
			rw.WriteHeader(http.StatusTeapot)
			_, _ = rw.Write(make([]byte, 65<<10))
		case "content.idx":
			itemkit.ServeContent(rw, r, "content.idx", time.Time{}, strings.NewReader("composed"))
		case "raw.idx":
			if _, err := io.WriteString(rw, "own body"); err == nil {
				t.Error("a traced overlay wrote content")
			}
		case "file.idx":
			got.ServeItemFile(rw, r, st, itemkit.Found{Repo: c, Item: file}, p)
		case "member.idx", "entry.idx":
			clone := r.Clone(r.Context())
			clone.URL.Path = "/artifactory/libs-local/a/b.txt"
			if req.Path == "entry.idx" {
				clone.URL.Path = "/artifactory/libs-local/z.zip!/x.txt"
			}
			clone.URL.RawPath = ""
			got.GetItem(rw, clone)
		case "cached.idx":
			cachedItem := model.Item{RepoKey: "remote", Cached: true, Name: "cached.idx"}
			if got.RemoteDeliveryAllowed(rw, r, source.Result{Item: &cachedItem}, p.Anonymous) {
				t.Error("a traced overlay's remote result was delivered")
			}
		case "stream.idx":
			stream := &source.Stream{Body: closer{func() { closed = true }}}
			if got.RemoteDeliveryAllowed(rw, r, source.Result{Stream: stream}, p.Anonymous) {
				t.Error("a traced overlay's stream was delivered")
			}
		default:
			return false
		}
		return true
	}
	k = w.Kit(itemkit.Deps{Overlays: []itemkit.Overlay{overlay}})
	user := kernel.Principal{Name: "user"}
	retrieving := "Retrieving info from local repository 'libs-local' type generic"
	found := func(first ...string) []string {
		return append(append([]string{"Received request", retrieving}, first...),
			"Identified requested resource as a file", "Responding with found resource",
			"Request succeeded")
	}
	for _, tc := range []struct {
		path   string
		caller kernel.Principal
		want   []string
	}{
		{"miss.idx", kittest.Admin, []string{
			"Received request", retrieving, "Unable to find resource in libs-local:miss.idx",
			"Responding with unfound resource",
			"Sending a response with the status '404' and the message " +
				"'File not found.; Path: 'libs-local:miss.idx''",
		}},
		{"refused.idx", user, []string{
			"Received request",
			"Sending a response with the status '403' and the message 'Download request for " +
				"repo:path 'libs-local:refused.idx' is forbidden for user: 'user'.; " +
				"Path: 'libs-local:refused.idx''",
		}},
		{"plain.idx", kittest.Admin, []string{
			"Received request", retrieving,
			"Sending a response with the status '500' and the message 'Internal Server Error'",
		}},
		{"big.idx", kittest.Admin, []string{
			"Received request", retrieving,
			"Sending a response with the status '418' and the message 'I'm a teapot'",
		}},
		{"content.idx", kittest.Admin, found()},
		{"raw.idx", kittest.Admin, found()},
		{"file.idx", kittest.Admin, found("Returning found resource from libs-local:a/b.txt")},
		{"member.idx", kittest.Admin, found("Returning found resource from libs-local:a/b.txt")},
		{"entry.idx", kittest.Admin, found("Returning found resource from libs-local:z.zip")},
		{"cached.idx", kittest.Admin, found("Returning found resource from remote-cache:cached.idx")},
		{"stream.idx", kittest.Admin, found("Returning validated transient resource")},
		{"a/b.txt", kittest.Admin, found("Returning found resource from libs-local:a/b.txt")},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := kittest.Request(t, http.MethodGet, "/artifactory/libs-local/"+tc.path+"?trace")
			if tc.caller.Name != kittest.Admin.Name {
				r = r.WithContext(accessapi.WithCaller(r.Context(), tc.caller,
					&accessapitest.Decider{AllowsFolderFunc: func(
						accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
					) bool {
						return true
					}}))
			}
			rec := httptest.NewRecorder()
			k.GetItem(rec, r)
			_, steps, ok := strings.Cut(rec.Body.String(), "Steps: \n")
			if rec.Code != http.StatusOK || !ok {
				t.Fatalf("answered %d %q", rec.Code, rec.Body)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSuffix(steps, "\n"), "\n") {
				_, step, _ := strings.Cut(line, " ")
				got = append(got, step)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf(
					"steps\n%s\nwant\n%s",
					strings.Join(got, "\n"),
					strings.Join(tc.want, "\n"),
				)
			}
		})
	}
	if !closed {
		t.Error("a traced overlay's stream was left open")
	}
	for _, it := range []model.Item{file, archive} {
		st, err := w.Store.Items().Stats(t.Context(), it.ID)
		if err != nil || st.DownloadCount != 0 {
			t.Errorf("downloads of %s %+v, %v; a trace counts none", it.Name, st, err)
		}
	}
}

// closer is a stream body that reports its Close.
type closer struct{ onClose func() }

func (closer) Read([]byte) (int, error) { return 0, io.EOF }

func (c closer) Close() error {
	c.onClose()
	return nil
}

// TestUnitTraceRefusesAsGetItemDoes: a caller who may read a file's folder
// but not the file is refused by ?trace as GetItem refuses them, with the
// download's refusal of a local file, missing or stored, and of the file a
// checksum names (#1572), and, through a virtual repository, with the
// refusal of the first member that holds the path it walks, whose file the
// checksum or archive entry names, under the virtual's path (#1533; before,
// 404 "Could not find resource" and 403 "Forbidden"). 7.161.26's
// RepoFilter.doGetOrHeadRequest traces the download itself, so the trace's
// last step is the download's answer. Before, the trace answered the
// missing file and the checksum 404 "Could not find resource", which GET
// refuses, and the stored file 403 "Forbidden".
func TestUnitTraceRefusesAsGetItemDoes(t *testing.T) {
	w := kittest.New(t, inmem.New())
	w.Put(t, w.Local, false, "a/b.txt", []byte("generic"))
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("x.txt")
	if err == nil {
		_, err = entry.Write([]byte("x"))
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	w.Put(t, w.Local, false, "z.zip", archive.Bytes())
	k := w.Kit(itemkit.Deps{})
	folderOnly := &accessapitest.Decider{AllowsFolderFunc: func(
		accessapi.ResourceType, accessapi.Repo, string, accessapi.Actions,
	) bool {
		return true
	}}
	serve := func(target string) *httptest.ResponseRecorder {
		r := kittest.Request(t, http.MethodGet, "/artifactory/"+target)
		r = r.WithContext(
			accessapi.WithCaller(r.Context(), kernel.Principal{Name: "user"}, folderOnly),
		)
		rec := httptest.NewRecorder()
		k.GetItem(rec, r)
		return rec
	}
	for _, tc := range []struct {
		target  string
		status  int
		message string
	}{
		{"libs-local/a/missing.txt", http.StatusForbidden, "Download request for repo:path " +
			"'libs-local:a/missing.txt' is forbidden for user: 'user'.; Path: 'libs-local:a/missing.txt'"},
		{"libs-local/a/b.txt", http.StatusForbidden, "Download request for repo:path " +
			"'libs-local:a/b.txt' is forbidden for user: 'user'.; Path: 'libs-local:a/b.txt'"},
		{"libs-local/a/b.txt.sha1", http.StatusForbidden, "Download request for repo:path " +
			"'libs-local:a/b.txt' is forbidden for user: 'user'.; Path: 'libs-local:a/b.txt'"},
		{"libs/a/b.txt.sha1", http.StatusForbidden, "Download request for repo:path " +
			"'libs-local:a/b.txt' is forbidden for user: 'user'.; Path: 'libs:a/b.txt'"},
		{"libs/z.zip!/x.txt", http.StatusForbidden, "Download request for repo:path " +
			"'libs-local:z.zip' is forbidden for user: 'user'.; Path: 'libs:z.zip'"},
	} {
		got := serve(tc.target)
		if got.Code != tc.status || got.Body.String() != errorEnvelope(tc.status, tc.message) {
			t.Errorf(
				"GET %s: %d %s, want %d %q",
				tc.target,
				got.Code,
				got.Body,
				tc.status,
				tc.message,
			)
		}
		trace := serve(tc.target + "?trace").Body.String()
		last := fmt.Sprintf(" Sending a response with the status '%d' and the message '%s'\n",
			tc.status, tc.message)
		if !strings.HasSuffix(trace, last) {
			t.Errorf("%s?trace ends\n%s\nwant%s", tc.target, trace, last)
		}
	}
}

// errorEnvelope is the body apierror writes for status and message.
func errorEnvelope(status int, message string) string {
	rec := httptest.NewRecorder()
	httpkit.WriteErr(
		rec,
		httptest.NewRequest(http.MethodGet, "/", nil),
		apierror.New(status, message),
	)
	return rec.Body.String()
}

// Read authorization for RPM metadata remains on its declared folder;
// repository download policy names the actual file independently.
func TestUnitRPMMetadataSeparatesFolderReadFromFilePolicy(t *testing.T) {
	for _, name := range []string{"repodata/repomd.xml", "nested/repodata/" + strings.Repeat("a", 64) + "-primary.xml.gz"} {
		t.Run(name, func(t *testing.T) {
			q, err := itemkit.RPMSourceRequest(source.Request{Resource: source.Resource{Path: name, PermissionPath: "scope", Method: http.MethodGet}})
			if err != nil {
				t.Fatal(err)
			}
			if !q.Resource.PermissionFolder || q.Resource.PermissionPath != "scope" || q.Resource.Kind != source.ResourceKindMetadata {
				t.Fatalf("RPM metadata Read scope changed: %+v", q.Resource)
			}
			if q.Resource.PolicyPath != name {
				t.Fatalf("RPM metadata policy path=%q,want%q", q.Resource.PolicyPath, name)
			}
		})
	}
}

func TestUnitRemoteResourceMarksAddressedRepositoryFile(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/artifactory/remote/repodata/repomd.xml?repositoryFile=false", nil)
	r.Header.Set("X-Repository-File", "false")
	resource := itemkit.RemoteResource(r, "repodata/repomd.xml")
	q, err := itemkit.RPMSourceRequest(source.Request{Resource: resource})
	if err != nil || !q.Resource.RepositoryFile || q.Resource.Kind != source.ResourceKindMetadata {
		t.Fatal("physical file provenance was lost by metadata preparation or caller input", q.Resource, err)
	}
}

func TestUnitMetadataFilePreparersPreserveRepositoryFileProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		prepare    func(source.Request) (source.Request, error)
	}{
		{"P2", "p2.index", itemkit.P2SourceRequest},
		{"Alpine", "v3.20/main/x86_64/APKINDEX.tar.gz", itemkit.AlpineSourceRequest},
		{"CRAN", "src/contrib/PACKAGES", itemkit.CranSourceRequest},
		{"Opkg", "arm/Packages.gz", itemkit.OpkgSourceRequest},
		{"RPM", "repodata/repomd.xml", itemkit.RPMSourceRequest},
		{"Debian Release", "dists/stable/Release", itemkit.DebianSourceRequest},
		{"Debian Packages", "dists/stable/main/binary-amd64/Packages.gz", itemkit.DebianSourceRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := tc.prepare(source.Request{Resource: source.Resource{Kind: source.ResourceKindArtifact, Path: tc.path, Method: http.MethodGet}})
			if err != nil || q.Resource.Kind != source.ResourceKindMetadata || !q.Resource.RepositoryFile {
				t.Fatal("validated metadata file lost its file origin", q.Resource, err)
			}
		})
	}
}

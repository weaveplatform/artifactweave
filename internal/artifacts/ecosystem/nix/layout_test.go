package nix

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

const fixtureHash = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"

// A hosted repository keeps a store path's narinfo and archive in its
// binary-cache folder, as 7.x does and jf nix queries; a remote's cache
// keeps the paths the client asks for.
func TestUnitNixLayoutPaths(t *testing.T) {
	url := "nar/18b3x2jida0r524p8g2wpimc4dqp8qhbhcy36g160lqx0hbqhp40.nar.xz"
	stored := "binary-cache/" + fixtureHash + "/" + fixtureHash + ".narinfo"
	if got := InfoItem(fixtureHash, false); got != stored {
		t.Fatal(got)
	}
	if got := InfoItem(fixtureHash, true); got != fixturePath {
		t.Fatal(got)
	}
	want := "binary-cache/" + fixtureHash + "/18b3x2jida0r524p8g2wpimc4dqp8qhbhcy36g160lqx0hbqhp40.nar.xz"
	if got := ArchiveItem(fixtureHash, url, false); got != want {
		t.Fatal(got)
	}
	if got := ArchiveItem(fixtureHash, url, true); got != url {
		t.Fatal(got)
	}
	other := strings.Repeat("0", 32)
	for path, want := range map[string][2]bool{
		stored:      {true, false},
		fixturePath: {false, true},
		"binary-cache/" + other + "/" + fixturePath:                        {false, false},
		"binary-cache/" + fixtureHash + "x" + fixturePath:                  {false, false},
		"binary-cache/" + fixtureHash + "/" + fixturePath + "x":            {false, false},
		"cache/" + fixtureHash + "/" + fixturePath:                         {false, false},
		"binary-cache/" + fixtureHash + "/nar/" + fixturePath:              {false, false},
		"binary-cache/" + strings.ToUpper(fixtureHash) + "/" + fixturePath: {false, false},
	} {
		if IsInfoItem(path, false) != want[0] || IsInfoItem(path, true) != want[1] {
			t.Errorf(
				"%s: hosted %v cached %v",
				path,
				IsInfoItem(path, false),
				IsInfoItem(path, true),
			)
		}
	}
	for path, want := range map[string][2]bool{
		want:                                     {true, false},
		url:                                      {false, true},
		"binary-cache/" + fixtureHash + "/x.nar": {false, false},
		"binary-cache/" + fixtureHash + "/" + url:             {false, false},
		"binary-cache/" + strings.ToUpper(fixtureHash) + "/a": {false, false},
		"binary-cache/" + fixtureHash:                         {false, false},
	} {
		_, hosted := IsArchiveItem(path, false)
		_, cached := IsArchiveItem(path, true)
		if hosted != want[0] || cached != want[1] {
			t.Errorf("%s: hosted %v cached %v", path, hosted, cached)
		}
	}
	info, _, _ := fixture(t)
	if info.Hash() != fixtureHash {
		t.Fatal(info.Hash())
	}
	data := info.Encode(info.URL)
	if parsed, err := Parse(data, stored); err != nil || parsed.StorePath != info.StorePath {
		t.Fatalf("stored narinfo: %v", err)
	}
	for _, path := range []string{
		"binary-cache/" + other + "/" + fixturePath,
		"binary-cache/" + other + "/" + other + ".narinfo",
		"nar/" + fixturePath,
	} {
		if _, err := Parse(data, path); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func rejection(t *testing.T, err error, status int, message string) {
	t.Helper()
	var r *format.RejectionError
	if !errors.As(err, &r) || r.Status != status || r.Message != message {
		t.Fatalf("got %v, want %d %q", err, status, message)
	}
}

// Inspect admits a narinfo only where the repository's layout stores it,
// and an archive at an ArchiveItem or, in a hosted repository, where the
// client uploads it, nar/<file>, until its narinfo is published.
func TestUnitNixInspectFollowsTheLayout(t *testing.T) {
	env, c, meta, archive, info := handlerFixture(t)
	h := Handler{}
	remote := c
	remote.Class = repo.Remote
	inspect := func(c repo.Config, path string, cached bool, it model.Item) error {
		_, err := h.Inspect(t.Context(), env, c, format.Write{
			Op: format.OpDeploy, Path: path, Cached: cached, Checksums: it.Actual, Size: it.Size,
		})
		return err
	}
	for _, ok := range []struct {
		c      repo.Config
		path   string
		cached bool
		it     model.Item
	}{
		{c, meta.FullPath(), false, meta},
		{c, archive.FullPath(), false, archive},
		{c, info.URL, false, archive},
		{remote, fixturePath, true, meta},
		{remote, info.URL, true, archive},
		// A remote's own write keeps its class's refusal, which Admit gives.
		{remote, fixturePath, false, meta},
	} {
		if err := inspect(ok.c, ok.path, ok.cached, ok.it); err != nil {
			t.Errorf("%s cached %v: %v", ok.path, ok.cached, err)
		}
	}
	rejection(t, inspect(c, fixturePath, false, meta), 400, "invalid Nix archive path")
	rejection(t, inspect(remote, meta.FullPath(), true, meta), 400, "invalid Nix archive path")
	rejection(
		t,
		inspect(remote, archive.FullPath(), true, archive),
		400,
		"invalid Nix archive path",
	)
	misnamed := "binary-cache/" + fixtureHash + "/" + strings.Repeat("0", 52) + ".nar.xz"
	rejection(
		t,
		inspect(c, misnamed, false, archive),
		409,
		"Nix archive checksum does not match its path",
	)
	if _, err := h.Admit(t.Context(), env, remote, format.Write{
		Op: format.OpDeploy, Path: fixturePath, Checksums: meta.Actual, Size: meta.Size,
	}); err == nil {
		t.Fatal("a remote's own write admitted")
	} else {
		rejection(t, err, 400, "unsupported Nix repository class")
	}
}

// held puts the fixture's archive content at the path the client uploads
// it to, nar/<file>, with its folder, as a hosted repository holds it
// until its narinfo is published.
func held(t *testing.T, env format.Env, c repo.Config, archive model.Item, path string) {
	t.Helper()
	for _, it := range []model.Item{
		{RepoKey: c.Key, Name: "nar", Folder: true},
		{RepoKey: c.Key, Name: path, Size: archive.Size, Actual: archive.Actual},
	} {
		if _, err := env.Store.Items().Put(t.Context(), it); err != nil {
			t.Fatal(err)
		}
	}
}

func underive(env format.Env) func(context.Context, repo.Config, bool, string) error {
	return func(ctx context.Context, c repo.Config, cached bool, path string) error {
		_, err := env.Store.Items().Delete(ctx, c.Key, cached, path)
		return err //nolint:wrapcheck // a test double
	}
}

func indexed(t *testing.T, env format.Env, c repo.Config) bool {
	t.Helper()
	_, err := env.Store.Packages().Find(t.Context(), c.Key, "weave-fixture")
	if err != nil && !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

// A hosted repository indexes a store path when its narinfo and archive
// are both in its folder, whichever arrives last, and never for an archive
// it holds where the client uploaded it.
func TestUnitNixHostedDeployedIndexesTheFolder(t *testing.T) {
	env, c, meta, archive, info := handlerFixture(t)
	h := Handler{}
	held(t, env, c, archive, info.URL)
	copyOf, err := env.Store.Items().Get(t.Context(), c.Key, false, info.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Deployed(t.Context(), env, c, copyOf); err != nil || indexed(t, env, c) {
		t.Fatalf("held archive indexed: %v", err)
	}
	if err = h.Deployed(t.Context(), env, c, archive); err != nil || !indexed(t, env, c) {
		t.Fatalf("archive after its narinfo: %v", err)
	}
	if _, err = env.Store.Items().Get(t.Context(), c.Key, false, info.URL); err != nil {
		t.Fatalf("an archive's arrival released the held copy: %v", err)
	}
	if _, err = env.Store.Items().Delete(t.Context(), c.Key, false, meta.FullPath()); err != nil {
		t.Fatal(err)
	}
	if err = h.Deployed(t.Context(), env, c, archive); err != nil {
		t.Fatalf("archive before its narinfo: %v", err)
	}
	other, err := env.Store.Items().Put(t.Context(), model.Item{
		RepoKey: c.Key, Name: InfoItem(fixtureHash, false), Size: meta.Size, Actual: meta.Actual,
	})
	if err != nil {
		t.Fatal(err)
	}
	misnamed := archive
	misnamed.ID, misnamed.Name = 0, strings.Repeat("0", 52)+".nar.xz"
	if misnamed, err = env.Store.Items().Put(t.Context(), misnamed); err != nil {
		t.Fatal(err)
	}
	env.Inspection = inspection{sha: misnamed.Actual.SHA256}
	if err = h.Deployed(t.Context(), env, c, misnamed); err != nil {
		t.Fatalf("an archive its narinfo does not name: %v", err)
	}
	broken := env
	broken.Blobs = brokenBlobs{Blobs: env.Blobs, open: true}
	if err = h.Deployed(t.Context(), broken, c, misnamed); !errors.Is(err, errDisk) {
		t.Fatalf("unreadable narinfo: %v", err)
	}
	broken = env
	broken.Store = faultDB{
		Store: env.Store,
		items: faultItems{ItemStore: env.Store.Items(), get: true},
	}
	if err = h.Deployed(t.Context(), broken, c, misnamed); !errors.Is(err, errDisk) {
		t.Fatalf("narinfo lookup fault: %v", err)
	}
	env.Inspection = nil
	if _, err = env.Store.Items().
		Delete(t.Context(), c.Key, false, archive.FullPath()); err != nil {
		t.Fatal(err)
	}
	env.Underive = func(context.Context, repo.Config, bool, string) error {
		t.Fatal("released a held copy without a stored archive")
		return nil
	}
	if err = h.Deployed(t.Context(), env, c, other); err != nil {
		t.Fatalf("narinfo before its archive: %v", err)
	}
	wrong := archive
	wrong.ID, wrong.Actual.SHA256 = 0, misnamed.Actual.SHA256+"0"
	if _, err = env.Store.Items().Put(t.Context(), wrong); err != nil {
		t.Fatal(err)
	}
	if err = h.Deployed(t.Context(), env, c, other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a mismatched archive: %v", err)
	}
}

// Publishing a narinfo in a hosted repository releases the archive it held
// for it at nar/<file>, with the nar folder once empty; a held file of
// other content stays, as does the folder while it holds another upload.
func TestUnitNixPublicationReleasesTheHeldArchive(t *testing.T) {
	env, c, meta, archive, info := handlerFixture(t)
	env.Underive = underive(env)
	h := Handler{}
	held(t, env, c, archive, info.URL)
	if err := h.Deployed(t.Context(), env, c, meta); err != nil || !indexed(t, env, c) {
		t.Fatal(err)
	}
	for _, path := range []string{info.URL, "nar"} {
		_, err := env.Store.Items().Get(t.Context(), c.Key, false, path)
		if !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s kept: %v", path, err)
		}
	}
	if _, err := env.Store.Items().Get(t.Context(), c.Key, false, archive.FullPath()); err != nil {
		t.Fatalf("stored archive released: %v", err)
	}
	held(t, env, c, archive, info.URL)
	another := archive
	another.ID, another.Path, another.Name = 0, "", "nar/"+strings.Repeat("0", 52)+".nar.xz"
	if _, err := env.Store.Items().Put(t.Context(), another); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(t.Context(), env, c, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.Items().Get(t.Context(), c.Key, false, "nar"); err != nil {
		t.Fatalf("a folder holding another upload was pruned: %v", err)
	}
	differs := another
	differs.ID, differs.Name, differs.Actual.SHA256 = 0, info.URL, "other"
	if _, err := env.Store.Items().Put(t.Context(), differs); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(t.Context(), env, c, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Store.Items().Get(t.Context(), c.Key, false, info.URL); err != nil {
		t.Fatalf("another upload's content released: %v", err)
	}
	if err := release(t.Context(), env, c, Info{URL: "nar/missing.nar"}); err != nil {
		t.Fatal(err)
	}
}

type releaseFaults struct {
	port.ItemStore
	get, children, remove error
}

func (f releaseFaults) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (model.Item, error) {
	if f.get != nil {
		return model.Item{}, f.get
	}
	return f.ItemStore.Get(ctx, key, cached, path)
}

func (f releaseFaults) Children(
	ctx context.Context, key string, cached bool, path string, page kernel.Page,
) ([]model.Item, string, error) {
	if f.children != nil {
		return nil, "", f.children
	}
	return f.ItemStore.Children(ctx, key, cached, path, page)
}

func (f releaseFaults) Delete(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (port.DeleteResult, error) {
	if f.remove != nil && path == "nar" {
		return port.DeleteResult{}, f.remove
	}
	return f.ItemStore.Delete(ctx, key, cached, path)
}

// A release that cannot finish fails the publication, except for a nar
// folder another release pruned first.
func TestUnitNixReleaseFaultsRemainFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		faults   releaseFaults
		underive error
		want     error
	}{
		"held lookup":    {faults: releaseFaults{get: errDisk}, want: errDisk},
		"underive":       {underive: errDisk, want: errDisk},
		"folder listing": {faults: releaseFaults{children: errDisk}, want: errDisk},
		"folder prune":   {faults: releaseFaults{remove: errDisk}, want: errDisk},
		"folder gone":    {faults: releaseFaults{children: kernel.ErrNotFound}},
		"folder pruned":  {faults: releaseFaults{remove: kernel.ErrNotFound}},
	} {
		t.Run(name, func(t *testing.T) {
			env, c, _, archive, info := handlerFixture(t)
			held(t, env, c, archive, info.URL)
			env.Underive = underive(env)
			if tc.underive != nil {
				env.Underive = func(context.Context, repo.Config, bool, string) error { return tc.underive }
			}
			tc.faults.ItemStore = env.Store.Items()
			env.Store = faultDB{Store: env.Store, items: tc.faults}
			if err := release(
				t.Context(),
				env,
				c,
				info,
			); !errors.Is(err, tc.want) ||
				(tc.want == nil) != (err == nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// A remote's cache indexes a store path from the narinfo and archive it
// caches at the client's paths.
func TestUnitNixCachedDeployedKeepsTheClientsPaths(t *testing.T) {
	env, c, meta, archive, info := handlerFixture(t)
	c.Class = repo.Remote
	cachedMeta, cachedArchive := meta, archive
	cachedMeta.ID, cachedMeta.Path, cachedMeta.Name, cachedMeta.Cached = 0, "", fixturePath, true
	cachedArchive.ID, cachedArchive.Path, cachedArchive.Name, cachedArchive.Cached = 0, "", info.URL, true
	var err error
	for _, it := range []*model.Item{&cachedArchive, &cachedMeta} {
		if *it, err = env.Store.Items().Put(t.Context(), *it); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler{}
	if err = h.Deployed(t.Context(), env, c, cachedArchive); err != nil || !indexed(t, env, c) {
		t.Fatalf("cached archive: %v", err)
	}
	if err = h.Reindex(t.Context(), env, c); err != nil || !indexed(t, env, c) {
		t.Fatalf("cached reindex: %v", err)
	}
	if _, err = h.Resolve(
		t.Context(),
		env,
		c,
		format.Download{Path: fixturePath, Cached: true},
	); err == nil {
		t.Fatal("generic remote read served")
	} else {
		rejection(t, err, 400, "remote Nix reads require the native binary-cache endpoint")
	}
}

// A generic read of a hosted narinfo needs its stored archive.
func TestUnitNixResolveNeedsTheStoredArchive(t *testing.T) {
	env, c, meta, archive, _ := handlerFixture(t)
	var seen string
	if _, err := (Handler{}).Resolve(t.Context(), env, c, format.Download{
		Path: meta.FullPath(), Readable: func(path string) bool { seen = path; return true },
	}); err != nil || seen != archive.FullPath() {
		t.Fatalf("checked %q: %v", seen, err)
	}
	if _, err := env.Store.Items().
		Delete(t.Context(), c.Key, false, archive.FullPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := (Handler{}).Resolve(
		t.Context(),
		env,
		c,
		format.Download{Path: meta.FullPath()},
	); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("narinfo without its archive: %v", err)
	}
}

package nixhttp_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pagedItems lists one item per page, so a channel's snapshots are read
// across pages.
type pagedItems struct{ port.ItemStore }

func (s pagedItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page kernel.Page,
) ([]artmodel.Item, string, error) {
	page.Limit = 1
	return s.ItemStore.Descendants(ctx, key, cached, path, page)
}

// failGets fails every item lookup by path.
type failGets struct{ port.ItemStore }

func (failGets) Get(context.Context, string, bool, string) (artmodel.Item, error) {
	return artmodel.Item{}, io.ErrUnexpectedEOF
}

func (f *fixture) createNix(t *testing.T, configs ...repo.Config) {
	t.Helper()
	for _, c := range configs {
		c.PackageType = repo.Nix
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
}

// The channel names the snapshot uploaded last however many pages its
// folder lists over, and a file in a snapshot's folder that is not its
// tarball is no snapshot.
func TestAccNixChannelReadsEveryPage(t *testing.T) {
	f := newFixture(t)
	f.st = failStore{serverStore: f.db, items: pagedItems{f.db.Items()}}
	f.mount(nil)
	status(t, f.do("PUT", snapshotDir(prefix, older), channelTarball(t, "first\n"), ""), 201)
	status(t, f.do("PUT", snapshotDir(prefix, newer), channelTarball(t, "second\n"), ""), 201)
	if _, err := f.db.Items().Put(t.Context(), artmodel.Item{
		RepoKey: "local", Name: "channels/" + channel + "/nixpkgs/" + older + "/notes.txt",
	}); err != nil {
		t.Fatal(err)
	}
	wantLocation(t, f, prefix+"channels/"+channel, "", channel+"/nixpkgs/"+newer)
}

// A caller is sent to the newest snapshot whose tarball they may read, and
// a virtual repository passes over a member the caller may not read.
func TestAccNixChannelNamesOnlyReadableSnapshots(t *testing.T) {
	f := newFixture(t)
	f.createNix(t,
		repo.Config{Key: "second", Class: repo.Local},
		repo.Config{Key: "virtual", Class: repo.Virtual, Repositories: []string{"local", "second"}},
	)
	first, second := channelTarball(t, "first\n"), channelTarball(t, "second\n")
	status(t, f.do("PUT", snapshotDir(prefix, older), first, ""), 201)
	status(t, f.do("PUT", snapshotDir(prefix, newer), first, ""), 201)
	status(t, f.do("PUT", snapshotDir("/artifactory/api/nix/second/", older), second, ""), 201)
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "read",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{
					"local": {
						Include: []string{"channels/" + channel + "/nixpkgs/" + older + "/**"},
					},
					"second":  {},
					"virtual": {},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	wantLocation(t, f, prefix+"channels/"+channel, "reader", channel+"/nixpkgs/"+older)
	status(t, f.do("GET", snapshotDir(prefix, newer), nil, "reader"), 403)
	status(t, f.do("GET", snapshotDir(prefix, newer)+"/"+tarball, nil, "reader"), 403)
	if err := f.db.Permissions().Delete(t.Context(), "read"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "read",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead}},
				Targets: map[string]accesstest.Target{"second": {}, "virtual": {}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/nix/virtual/"
	wantLocation(t, f, base+"channels/"+channel, "reader", channel+"/nixpkgs/"+older)
	w := f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, "reader")
	status(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), second) {
		t.Fatal("tarball served from a member the caller may not read")
	}
	status(t, f.do("GET", snapshotDir(base, newer), nil, "reader"), 404)
}

// A folder where a snapshot's tarball belongs is no snapshot: the channel
// does not name it, its page is not found, and a virtual repository serves
// the snapshot from the next member that holds it.
func TestAccNixChannelFolderIsNoSnapshot(t *testing.T) {
	f := newFixture(t)
	f.createNix(t,
		repo.Config{Key: "second", Class: repo.Local},
		repo.Config{Key: "virtual", Class: repo.Virtual, Repositories: []string{"local", "second"}},
	)
	data := channelTarball(t, "second\n")
	status(t, f.do("PUT", snapshotDir("/artifactory/api/nix/second/", older), data, ""), 201)
	if _, err := f.db.Items().Put(t.Context(), artmodel.Item{
		RepoKey: "local",
		Name:    "channels/" + channel + "/nixpkgs/" + older + "/" + tarball,
		Folder:  true,
	}); err != nil {
		t.Fatal(err)
	}
	status(t, f.do("GET", prefix+"channels/"+channel, nil, ""), 404)
	status(t, f.do("GET", snapshotDir(prefix, older), nil, ""), 404)
	base := "/artifactory/api/nix/virtual/"
	status(t, f.do("GET", snapshotDir(base, older), nil, ""), 200)
	w := f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, "")
	status(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("tarball from the wrong member")
	}
}

// A store fault is a server error, not a channel or snapshot that is
// missing, through a hosted repository and a virtual one; a member of the
// wrong package type is refused as it is for the binary cache.
func TestAccNixChannelFaultsDoNotLookLikeMisses(t *testing.T) {
	f := newFixture(t)
	f.createNix(
		t,
		repo.Config{Key: "virtual", Class: repo.Virtual, Repositories: []string{"local"}},
	)
	status(t, f.do("PUT", snapshotDir(prefix, older), channelTarball(t, "first\n"), ""), 201)
	base := "/artifactory/api/nix/virtual/"
	f.st = failStore{serverStore: f.db, items: failItems{f.db.Items()}}
	f.mount(nil)
	status(t, f.do("GET", prefix+"channels/"+channel, nil, ""), 500)
	status(t, f.do("GET", base+"channels/"+channel, nil, ""), 500)
	f.st = failStore{serverStore: f.db, items: failGets{f.db.Items()}}
	f.mount(nil)
	status(t, f.do("GET", snapshotDir(prefix, older), nil, ""), 500)
	f.st = failStore{serverStore: f.db, repos: failRepos{f.db.Repos(), "local"}}
	f.mount(nil)
	for _, path := range []string{"channels/" + channel, "channels/" + channel + "/nixpkgs/" + older} {
		status(t, f.do("GET", base+path, nil, ""), 500)
	}
	f.st = f.db
	f.mount(nil)
	if err := f.db.Repos().
		Create(t.Context(), repo.Config{Key: "other", Class: repo.Local, PackageType: repo.Generic}); err != nil {
		t.Fatal(err)
	}
	f.patch("virtual", func(c *repo.Config) { c.Repositories = []string{"other"} })
	status(t, f.do("GET", base+"channels/"+channel, nil, ""), 400)
}

// channelSource answers a remote's channel reads with a release URL under
// the default releases origin, and its tarball reads with data.
type channelSource struct {
	mode, release string
	data          []byte
}

func (*channelSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s *channelSource) Read(
	_ *http.Request,
	_ repo.Config,
	res source.Resource,
) (source.Result, error) {
	if res.Kind == source.ResourceKindArtifact && s.mode == "tarball unavailable" {
		return source.Result{}, source.ErrUnavailable
	}
	if s.mode == "unavailable" {
		return source.Result{}, source.ErrUnavailable
	}
	result := source.Result{
		Metadata: source.MetadataResponse{BaseURL: s.release},
		Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(s.data)), Status: 200, Size: int64(len(s.data)),
		},
		CheckRead: func(context.Context, string) error { return nil },
		Revalidate: func(context.Context) error {
			if s.mode == "revoked" {
				return source.ErrDenied
			}
			return nil
		},
	}
	result.CheckPermission = result.CheckRead
	if s.mode == "missing revalidation" {
		result.Revalidate = nil
	}
	return result, nil
}

// A remote's channel read must come back with a release under the
// registry's releases origin (https://releases.nixos.org by default) and a
// revalidation that passes; otherwise the channel, its page and its
// tarball fail closed.
func TestAccNixRemoteChannelRequiresValidatedAuthority(t *testing.T) {
	releaseURL := "https://releases.nixos.org/nixos/25.05/" + release1
	for _, tc := range []struct {
		mode, release      string
		channel, page, tar int
	}{
		{"", releaseURL, 302, 200, 200},
		{"", "https://releases.example/nixos/25.05/" + release1, 502, 502, 502},
		{"missing revalidation", releaseURL, 502, 502, 502},
		{"revoked", releaseURL, 403, 403, 403},
		{"unavailable", releaseURL, 502, 502, 502},
		{"tarball unavailable", releaseURL, 302, 200, 502},
	} {
		t.Run(tc.mode+tc.release, func(t *testing.T) {
			f := newFixture(t)
			f.createNix(
				t,
				repo.Config{Key: "remote", Class: repo.Remote, URL: "https://cache.example/nix"},
			)
			src := &channelSource{
				mode:    tc.mode,
				release: tc.release,
				data:    channelTarball(t, "remote\n"),
			}
			f.mount(nil, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
			base := "/artifactory/api/nix/remote/"
			status(t, f.do("GET", base+"channels/"+official, nil, ""), tc.channel)
			status(t, f.do("GET", remoteSnapshot(base, release1), nil, ""), tc.page)
			status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), tc.tar)
		})
	}
}

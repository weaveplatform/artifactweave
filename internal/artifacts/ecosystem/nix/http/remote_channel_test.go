package nixhttp_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// An official channel and two of its releases, as channels.nixos.org
// names them.
const (
	official = "nixos-25.05"
	release1 = "nixos-25.05.813814.ac62194c3917"
	release2 = "nixos-25.05.813900.0123456789ab"
)

// remoteChannelFixture is remoteFixture with the channel registry and its
// releases on the fake upstream, under /channels and /releases.
func remoteChannelFixture(t *testing.T) (*fixture, *fakeupstream.Server, repo.Config, string) {
	t.Helper()
	f, up, c, base := remoteFixture(t)
	var err error
	if f.channels, err = url.Parse(up.URL + "/channels"); err != nil {
		t.Fatal(err)
	}
	if f.releases, err = url.Parse(up.URL + "/releases"); err != nil {
		t.Fatal(err)
	}
	f.mount(f.remote)
	return f, up, c, base
}

// publishRelease points channel name at release id, as the registry does:
// /channels/<name> redirects to the release, which answers a page and holds
// the tarball.
func publishRelease(up *fakeupstream.Server, name, id string, data []byte) string {
	at := "/releases/nixos/25.05/" + id
	up.Set("/channels/"+name, fakeupstream.Resource{Redirect: up.URL + at})
	up.Set(at, fakeupstream.Resource{
		Header: http.Header{"Content-Type": {"text/html"}},
		Body:   []byte("<html>" + id + "</html>"),
	})
	up.Set(at+"/"+tarball, fakeupstream.Resource{Body: data})
	return at + "/" + tarball
}

func remoteSnapshot(base, id string) string {
	return base + "channels/" + official + "/nixos/" + id
}

// A remote repository resolves an official channel name at the channel
// registry, whatever its own URL, redirects to the release the registry
// names, and serves and caches that release's tarball, validated as an
// upload is. Read permission comes first; a name the registry does not
// know is not found, and a remote takes no upload.
func TestAccNixRemoteChannelFromTheRegistry(t *testing.T) {
	f, up, _, base := remoteChannelFixture(t)
	data := channelTarball(t, "remote\n")
	upstreamTarball := publishRelease(up, official, release1, data)
	status(t, f.do("GET", base+"channels/"+official, nil, "denied"), 403)
	status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, "anonymous"), 401)
	if up.Count("", "") != 0 {
		t.Fatal("a refused read reached the registry")
	}
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release1)
	page := f.do("GET", remoteSnapshot(base, release1), nil, "")
	status(t, page, 200)
	if !bytes.Contains(page.Body.Bytes(), []byte(release1+"/"+tarball)) {
		t.Fatalf("snapshot page %q", page.Body)
	}
	for range 2 {
		w := f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, "")
		status(t, w, 200)
		if !bytes.Equal(w.Body.Bytes(), data) {
			t.Fatal("tarball changed")
		}
	}
	if n := up.Count("GET", upstreamTarball); n != 1 {
		t.Fatalf("tarball fetched %d times", n)
	}
	if _, err := f.db.Items().Get(t.Context(), "remote", true,
		"channels/"+official+"/nixos/"+release1+"/"+tarball); err != nil {
		t.Fatalf("tarball not cached: %v", err)
	}
	status(t, f.do("GET", remoteSnapshot(base, release2), nil, ""), 404)
	status(t, f.do("GET", remoteSnapshot(base, release2)+"/"+tarball, nil, ""), 404)
	status(t, f.do("GET", base+"channels/nixos-unknown", nil, ""), 404)
	wantBody(t, f.do("PUT", remoteSnapshot(base, release1), data, ""), 405,
		"Nix publication requires a hosted repository")
}

// A registry answer that is not a release, a hop outside the registry and
// its releases, and a tarball that is not one fail closed with 502, and the
// bad tarball is not cached.
func TestAccNixRemoteChannelRejectsBadUpstreams(t *testing.T) {
	f, up, _, base := remoteChannelFixture(t)
	up.Set("/channels/flat", fakeupstream.Resource{Body: []byte("<html></html>")})
	up.Set(
		"/channels/away",
		fakeupstream.Resource{Redirect: "https://invalid.example/nixos/25.05/" + release1},
	)
	up.Set(
		"/channels/odd",
		fakeupstream.Resource{Redirect: up.URL + "/releases/flakes/25.05/" + release1},
	)
	up.Set("/channels/short", fakeupstream.Resource{Redirect: up.URL + "/releases/" + release1})
	for _, at := range []string{"/releases/flakes/25.05/" + release1, "/releases/" + release1} {
		up.Set(at, fakeupstream.Resource{
			Header: http.Header{"Content-Type": {"text/html"}}, Body: []byte("<html></html>"),
		})
	}
	for _, name := range []string{"flat", "away", "odd", "short"} {
		t.Run(name, func(t *testing.T) {
			status(t, f.do("GET", base+"channels/"+name, nil, ""), 502)
			status(t, f.do("GET", base+"channels/"+name+"/nixos/"+release1, nil, ""), 502)
		})
	}
	publishRelease(up, official, release1, []byte("not a tarball"))
	status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 502)
	if _, err := f.db.Items().Get(t.Context(), "remote", true,
		"channels/"+official+"/nixos/"+release1+"/"+tarball); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("bad tarball cached: %v", err)
	}
	// The tarball must come from the releases origin, even by redirect.
	up.Set("/releases/nixos/25.05/"+release1+"/"+tarball, fakeupstream.Resource{
		Redirect: up.URL + "/channels/" + tarball,
	})
	up.Set("/channels/"+tarball, fakeupstream.Resource{Body: channelTarball(t, "elsewhere\n")})
	status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 502)
}

// When the registry moves the channel on, the remote follows it once its
// cached answer is due for refresh, and serves the new release; the old
// release is no longer served [I]. With the upstream gone and the remote
// offline, the cached channel and tarball are still served.
func TestAccNixRemoteChannelFollowsAndWorksOffline(t *testing.T) {
	f, up, c, base := remoteChannelFixture(t)
	first, second := channelTarball(t, "first\n"), channelTarball(t, "second\n")
	publishRelease(up, official, release1, first)
	c.RetrievalCachePeriodSecs = new(0)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release1)
	status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 200)
	publishRelease(up, official, release2, second)
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release2)
	w := f.do("GET", remoteSnapshot(base, release2)+"/"+tarball, nil, "")
	status(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), second) {
		t.Fatal("old release served")
	}
	status(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 404)
	c.Offline = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	up.Close()
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release2)
	status(t, f.do("GET", remoteSnapshot(base, release2), nil, ""), 200)
	status(t, f.do("GET", remoteSnapshot(base, release2)+"/"+tarball, nil, ""), 200)
}

// A blacked-out remote refuses the channel and the tarball with the
// policy's 404 before the registry is asked, naming the path asked for;
// patterns that reject the tarball refuse it with theirs and leave the
// channel served.
func TestAccNixRemoteChannelPolicy(t *testing.T) {
	f, up, c, base := remoteChannelFixture(t)
	publishRelease(up, official, release1, channelTarball(t, "remote\n"))
	stored := "channels/" + official + "/nixos/" + release1 + "/" + tarball
	on := true
	c.BlackedOut = &on
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	status(t, f.do("GET", base+"channels/"+official, nil, "denied"), 403)
	wantBody(t, f.do("GET", base+"channels/"+official, nil, ""), 404,
		blackedOut("remote", "channels/"+official))
	wantBody(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 404,
		blackedOut("remote", stored))
	if up.Count("", "") != 0 {
		t.Fatal("a blacked-out remote asked the registry")
	}
	exclude := "channels/**/" + tarball
	c.BlackedOut, c.ExcludesPattern = nil, &exclude
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release1)
	wantBody(t, f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, ""), 404,
		rejected("remote", stored))
	if up.Count("", "/releases/nixos/25.05/"+release1+"/"+tarball) != 0 {
		t.Fatal("a refused tarball was fetched")
	}
}

// A virtual repository follows a remote member's channel when no earlier
// member has it, and serves the release's tarball through that member.
func TestAccNixVirtualFollowsARemoteMembersChannel(t *testing.T) {
	f, up, _, _ := remoteChannelFixture(t)
	data := channelTarball(t, "remote\n")
	publishRelease(up, official, release1, data)
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix,
		Repositories: []string{"local", "remote"},
	})); err != nil {
		t.Fatal(err)
	}
	status(t, f.do("PUT", snapshotDir(prefix, older), channelTarball(t, "local\n"), ""), 201)
	base := "/artifactory/api/nix/virtual/"
	wantLocation(t, f, base+"channels/"+official, "", official+"/nixos/"+release1)
	status(t, f.do("GET", remoteSnapshot(base, release1), nil, ""), 200)
	w := f.do("GET", remoteSnapshot(base, release1)+"/"+tarball, nil, "")
	status(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("tarball changed")
	}
	wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+older)
	status(t, f.do("GET", remoteSnapshot(base, release2)+"/"+tarball, nil, ""), 404)
}

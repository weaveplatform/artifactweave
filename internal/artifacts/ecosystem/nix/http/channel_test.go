package nixhttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The channel and snapshots of the JFrog documentation's upload example.
const (
	channel   = "nixpkgs-25.11"
	older     = "nixpkgs-25.11pre893393"
	newer     = "nixpkgs-25.11pre893400"
	tarball   = "nixexprs.tar.xz"
	invalidCh = "invalid Nix channel archive"
)

// snapshotDir is snapshot id's folder in channel under base: the URL the
// JFrog documentation uploads it to and the channel redirects to.
func snapshotDir(base, id string) string {
	return base + "channels/" + channel + "/nixpkgs/" + id
}

// tarred is a tar archive of one top-level folder holding a default.nix and
// a marker file, as nix-channel unpacks a channel.
func tarred(t *testing.T, marker string) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := tw.WriteHeader(
		&tar.Header{Name: "nixexprs/", Typeflag: tar.TypeDir, Mode: 0o755},
	); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"default.nix": "{ ... }: { }\n", "marker": marker} {
		if err := tw.WriteHeader(&tar.Header{
			Name: "nixexprs/" + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func xzOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// channelTarball is a nixexprs.tar.xz whose marker file holds marker.
func channelTarball(t *testing.T, marker string) []byte {
	t.Helper()
	return xzOf(t, tarred(t, marker))
}

func wantLocation(t *testing.T, f *fixture, path, user, want string) {
	t.Helper()
	w := f.do("GET", path, nil, user)
	status(t, w, 302)
	if got := w.Header().Get("Location"); got != want {
		t.Fatalf("Location %q want %q", got, want)
	}
}

// A channel snapshot uploads to the folder the JFrog documentation names
// and is stored there as nixexprs.tar.xz. The channel redirects to the
// snapshot uploaded last, whose folder answers a page and whose tarball is
// served, so nix-channel --update unpacks it; a newer upload moves the
// channel on, and uploading an older snapshot again moves it back [I].
// Before the fix every one of these answered 404, and the upload 400.
func TestAccNixChannelPublishAndFollow(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "fed", Class: repo.Federated, PackageType: repo.Nix,
	})); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"local", "fed"} {
		t.Run(key, func(t *testing.T) {
			base := "/artifactory/api/nix/" + key + "/"
			first, second := channelTarball(t, "first\n"), channelTarball(t, "second\n")
			status(t, f.do("PUT", snapshotDir(base, older), first, ""), 201)
			stored, err := f.db.Items().Get(t.Context(), key, false,
				"channels/"+channel+"/nixpkgs/"+older+"/"+tarball)
			if err != nil || stored.Size != int64(len(first)) {
				t.Fatalf("snapshot not stored as %s: %v", tarball, err)
			}
			wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+older)
			wantLocation(t, f, base+"channels/"+channel+"/", "", "nixpkgs/"+older)
			head := f.do("HEAD", base+"channels/"+channel, nil, "")
			status(t, head, 302)
			page := f.do("GET", snapshotDir(base, older), nil, "")
			status(t, page, 200)
			if page.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
				!bytes.Contains(page.Body.Bytes(), []byte(`href="`+older+"/"+tarball+`"`)) {
				t.Fatalf("snapshot page %q", page.Body)
			}
			got := f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, "")
			status(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), first) {
				t.Fatal("tarball changed")
			}
			status(t, f.do("HEAD", snapshotDir(base, older)+"/"+tarball, nil, ""), 200)
			// nix-channel asks for .tar.zst first and moves on when it is
			// missing.
			status(t, f.do("GET", snapshotDir(base, older)+"/nixexprs.tar.zst", nil, ""), 404)
			status(t, f.do("GET", snapshotDir(base, newer), nil, ""), 404)
			status(t, f.do("PUT", snapshotDir(base, newer)+"/"+tarball, second, ""), 201)
			wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+newer)
			got = f.do("GET", snapshotDir(base, newer)+"/"+tarball, nil, "")
			status(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), second) {
				t.Fatal("newer tarball changed")
			}
			status(t, f.do("PUT", snapshotDir(base, older), first, ""), 201)
			wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+older)
			status(t, f.do("GET", base+"channels/nixos-25.05", nil, ""), 404)
		})
	}
}

// An upload that is not an xz-compressed tar archive is refused with 400
// [I for the status and message]; so is one too short to be a tarball. A
// caller who may not write is refused first, and a virtual or remote
// repository does not accept uploads.
func TestAccNixChannelUploadRefusals(t *testing.T) {
	f := newFixture(t)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarred(t, "gzip\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	empty := new(bytes.Buffer)
	if err := tar.NewWriter(empty).Close(); err != nil {
		t.Fatal(err)
	}
	trailing := append(tarred(t, "trailing\n"), []byte("junk")...)
	for name, body := range map[string][]byte{
		"gzip":     gz.Bytes(),
		"plain":    tarred(t, "plain\n"),
		"text":     []byte("not a tarball"),
		"xz text":  xzOf(t, []byte("not a tarball")),
		"no entry": xzOf(t, empty.Bytes()),
		"trailing": xzOf(t, trailing),
		"appended": append(channelTarball(t, "appended\n"), 0),
		"empty":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			wantBody(t, f.do("PUT", snapshotDir(prefix, older), body, ""), 400, invalidCh)
		})
	}
	if _, err := f.db.Items().Get(t.Context(), "local", false,
		"channels/"+channel+"/nixpkgs/"+older+"/"+tarball); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("refused upload stored: %v", err)
	}
	good := channelTarball(t, "good\n")
	status(t, f.do("PUT", snapshotDir(prefix, older), good, "denied"), 403)
	status(t, f.do("PUT", snapshotDir(prefix, older), good, "anonymous"), 401)
	if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix, Repositories: []string{"local"},
	})); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do("PUT", snapshotDir("/artifactory/api/nix/virtual/", older), good, ""), 405,
		"Nix publication requires a hosted repository")
}

// The answers outside a well-formed channel, snapshot or tarball path stay
// as they were: a GET is not found, whoever asks, and an upload is refused
// as an invalid archive path. A tarball under another name is never
// stored.
func TestAccNixChannelKeepsOtherPathsUnchanged(t *testing.T) {
	f := newFixture(t)
	good := channelTarball(t, "good\n")
	for _, path := range []string{
		"channels",
		"channels/" + channel + "/nixpkgs",
		"channels/" + channel + "/nixpkgs/" + older + "/other",
		"channels/" + channel + "/nixpkgs/" + older + "/" + tarball + "/more",
		"channels/" + channel + "/flake/" + older,
		"channels/.hidden",
		"channels/.hidden/nixpkgs/" + older,
	} {
		for _, user := range []string{"", "denied", "anonymous"} {
			status(t, f.do("GET", prefix+path, nil, user), 404)
		}
		wantBody(t, f.do("PUT", prefix+path, good, ""), 400, "invalid Nix archive path")
	}
	wantBody(t, f.do("PUT", prefix+"channels/"+channel, good, ""), 400, "invalid Nix archive path")
	wantBody(t, f.do("PUT", snapshotDir(prefix, older)+"/nixexprs.tar.gz", good, ""), 400,
		"invalid Nix archive path")
}

// Read permission comes first (DV-057): a caller who may not read the
// channel or its tarball gets 403, or 401 when anonymous, whether the
// repository is blacked out or not. A blacked-out repository then refuses
// the channel, the snapshot page and the tarball with the policy's 404,
// naming the path asked for; patterns that reject the tarball refuse it
// with theirs while the channel still names the snapshot (R3).
func TestAccNixChannelPermissionAndPolicy(t *testing.T) {
	f := newFixture(t)
	status(t, f.do("PUT", snapshotDir(prefix, older), channelTarball(t, "first\n"), ""), 201)
	chanPath, dir := prefix+"channels/"+channel, snapshotDir(prefix, older)
	file := dir + "/" + tarball
	stored := "channels/" + channel + "/nixpkgs/" + older + "/" + tarball
	refusals := func() {
		t.Helper()
		for _, path := range []string{chanPath, dir, file} {
			status(t, f.do("GET", path, nil, "denied"), 403)
			status(t, f.do("GET", path, nil, "anonymous"), 401)
			status(t, f.do("HEAD", path, nil, "denied"), 403)
		}
	}
	refusals()
	on, off := true, false
	f.patch("local", func(c *repo.Config) { c.BlackedOut = &on })
	refusals()
	wantBody(t, f.do("GET", chanPath, nil, ""), 404, blackedOut("local", "channels/"+channel))
	wantBody(t, f.do("GET", dir, nil, ""), 404,
		blackedOut("local", "channels/"+channel+"/nixpkgs/"+older))
	wantBody(t, f.do("GET", file, nil, ""), 404, blackedOut("local", stored))
	exclude := "channels/**/" + tarball
	f.patch("local", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &exclude })
	refusals()
	wantBody(t, f.do("GET", file, nil, ""), 404, rejected("local", stored))
	wantLocation(t, f, chanPath, "", channel+"/nixpkgs/"+older)
	status(t, f.do("GET", dir, nil, ""), 200)
}

// A virtual repository redirects to the first member's newest snapshot in
// order, serves the snapshot's page and tarball from the first member that
// holds it, and passes over a member that is blacked out or whose patterns
// reject the tarball. Its own patterns refuse the tarball with "No
// repository found to serve the request".
func TestAccNixChannelVirtual(t *testing.T) {
	f := newFixture(t)
	for _, c := range []repo.Config{
		{Key: "empty", Class: repo.Local, PackageType: repo.Nix},
		{Key: "second", Class: repo.Local, PackageType: repo.Nix},
		{
			Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix,
			Repositories: []string{"missing", "empty", "local", "second"},
		},
	} {
		if err := f.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	first, second := channelTarball(t, "first\n"), channelTarball(t, "second\n")
	status(t, f.do("PUT", snapshotDir(prefix, older), first, ""), 201)
	status(t, f.do("PUT", snapshotDir("/artifactory/api/nix/second/", newer), second, ""), 201)
	base := "/artifactory/api/nix/virtual/"
	served := func(id string, want []byte) {
		t.Helper()
		status(t, f.do("GET", snapshotDir(base, id), nil, ""), 200)
		w := f.do("GET", snapshotDir(base, id)+"/"+tarball, nil, "")
		status(t, w, 200)
		if !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatal("tarball from the wrong member")
		}
	}
	wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+older)
	served(older, first)
	served(newer, second)
	status(t, f.do("GET", base+"channels/"+channel, nil, "denied"), 403)
	status(t, f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, "anonymous"), 401)
	on, off := true, false
	f.patch("local", func(c *repo.Config) { c.BlackedOut = &on })
	wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+newer)
	status(t, f.do("GET", snapshotDir(base, older), nil, ""), 404)
	status(t, f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, ""), 404)
	exclude := "channels/**/" + tarball
	f.patch("local", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &exclude })
	wantLocation(t, f, base+"channels/"+channel, "", channel+"/nixpkgs/"+older)
	status(t, f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, ""), 404)
	f.patch("local", func(c *repo.Config) { c.ExcludesPattern = nil })
	f.patch("virtual", func(c *repo.Config) { c.ExcludesPattern = &exclude })
	wantBody(
		t,
		f.do("GET", snapshotDir(base, older)+"/"+tarball, nil, ""),
		404,
		"No repository found to serve the request for virtual:channels/"+channel+"/nixpkgs/"+older+"/"+tarball,
	)
	f.patch("virtual", func(c *repo.Config) { c.ExcludesPattern = nil })
	f.patch("virtual", func(c *repo.Config) { c.Repositories = []string{"empty"} })
	status(t, f.do("GET", base+"channels/"+channel, nil, ""), 404)
	status(t, f.do("GET", snapshotDir(base, older), nil, ""), 404)
}

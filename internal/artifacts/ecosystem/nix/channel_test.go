package nix

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

func channelTar(t *testing.T, padding int) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	body := "marker\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "nixexprs/marker", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	out.Write(make([]byte, padding))
	return out.Bytes()
}

func compressXZ(t *testing.T, data []byte) []byte {
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

func TestUnitChannelPaths(t *testing.T) {
	s := Snapshot{Channel: "nixpkgs-25.11", Flavor: "nixpkgs", ID: "nixpkgs-25.11pre893393"}
	if s.Dir() != "channels/nixpkgs-25.11/nixpkgs/nixpkgs-25.11pre893393" ||
		s.Path() != s.Dir()+"/nixexprs.tar.xz" {
		t.Fatalf("paths %q %q", s.Dir(), s.Path())
	}
	if got, ok := ParseSnapshotDir(s.Dir()); !ok || got != s {
		t.Fatalf("dir parsed as %#v", got)
	}
	if got, ok := ParseSnapshotPath(s.Path()); !ok || got != s || !IsChannelPath(s.Path()) {
		t.Fatalf("path parsed as %#v", got)
	}
	nixos := Snapshot{
		Channel: "nixos-25.05-small",
		Flavor:  "nixos",
		ID:      "nixos-25.05.813814.ac62194c3917",
	}
	if !IsChannelPath(nixos.Path()) {
		t.Fatal("nixos snapshot refused")
	}
	for _, path := range []string{
		"",
		s.Dir(),
		s.Dir() + "/nixexprs.tar.gz",
		"channel/nixpkgs-25.11/nixpkgs/x/nixexprs.tar.xz",
		"channels/nixpkgs-25.11/nixpkgs/nixexprs.tar.xz",
		"channels/nixpkgs-25.11/flakes/x/nixexprs.tar.xz",
		"channels/.x/nixpkgs/x/nixexprs.tar.xz",
		"channels/x/nixpkgs/../nixexprs.tar.xz",
		"channels/x/nixpkgs/a b/nixexprs.tar.xz",
		"channels/x/nixpkgs/" + strings.Repeat("a", 201) + "/nixexprs.tar.xz",
		"channels/x/nixpkgs/y/z/nixexprs.tar.xz",
		"nar/" + strings.Repeat("0", 64) + ".nar",
	} {
		if IsChannelPath(path) {
			t.Errorf("%q accepted", path)
		}
	}
	if !ValidChannel("nixos-unstable") || ValidChannel("") || ValidChannel("-x") ||
		ValidFlavor("flakes") || !ValidFlavor("nixos") {
		t.Fatal("channel names or flavors misjudged")
	}
}

func TestUnitReadChannel(t *testing.T) {
	good := compressXZ(t, channelTar(t, 0))
	padded := compressXZ(t, channelTar(t, 9216))
	for name, data := range map[string][]byte{"tight": good, "padded": padded} {
		if err := ReadChannel(bytes.NewReader(data), int64(len(data))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	empty := new(bytes.Buffer)
	if err := tar.NewWriter(empty).Close(); err != nil {
		t.Fatal(err)
	}
	trailing := append(channelTar(t, 0), 1)
	// Cutting the end of the xz footer leaves the archive and its padding
	// readable and fails the stream after them.
	truncated := padded[:len(padded)-4]
	for name, tc := range map[string]struct {
		data []byte
		size int64
	}{
		"empty":       {nil, 0},
		"oversized":   {good, MaxArchiveBytes + 1},
		"short":       {good, int64(len(good)) + 1},
		"long":        {good, int64(len(good)) - 1},
		"not xz":      {channelTar(t, 0), int64(len(channelTar(t, 0)))},
		"not tar":     {compressXZ(t, []byte("plain text")), -1},
		"no entries":  {compressXZ(t, empty.Bytes()), -1},
		"after tar":   {compressXZ(t, trailing), -1},
		"cut stream":  {truncated, -1},
		"bad xz body": {append(good[:12:12], bytes.Repeat([]byte{0xff}, 64)...), -1},
	} {
		size := tc.size
		if size == -1 {
			size = int64(len(tc.data))
		}
		if err := ReadChannel(bytes.NewReader(tc.data), size); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A channel tarball is checked as it is written, whatever API carries it,
// and is neither indexed nor a reason to read the repository's narinfo.
func TestUnitChannelAdmissionAndIndex(t *testing.T) {
	env, c, _, _, _ := handlerFixture(t)
	path := "channels/nixpkgs-25.11/nixpkgs/nixpkgs-25.11pre893393/nixexprs.tar.xz"
	store := func(data []byte) model.Item {
		t.Helper()
		up, err := env.Blobs.(*fs.Store).BeginUpload(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = up.Append(t.Context(), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		bi, err := up.Commit(t.Context(), blob.Digests{})
		if err != nil {
			t.Fatal(err)
		}
		return model.Item{
			RepoKey: c.Key, Name: path, Size: bi.Size,
			Actual: kernel.Checksums{SHA256: bi.Digests.SHA256},
		}
	}
	bad := store([]byte("not a tarball"))
	_, err := (Handler{}).Inspect(t.Context(), env, c, format.Write{
		Op: format.OpDeploy, Path: path, Size: bad.Size, Checksums: bad.Actual,
	})
	var rejected *format.RejectionError
	if !errors.As(err, &rejected) || rejected.Status != 400 ||
		rejected.Message != "invalid Nix channel archive" {
		t.Fatalf("bad tarball: %v", err)
	}
	good := store(compressXZ(t, channelTar(t, 0)))
	w, err := (Handler{}).Admit(t.Context(), env, c, format.Write{
		Op: format.OpDeploy, Path: path, Size: good.Size, Checksums: good.Actual,
	})
	if err != nil || w.Path != path {
		t.Fatalf("good tarball: %v", err)
	}
	if (Handler{}).Classify(c, path) != format.ClassUnique {
		t.Fatal("a snapshot's tarball is not unique")
	}
	// The narinfo listing fails, so a Deployed that visited it would fail.
	broken := env
	broken.Store = faultDB{
		Store: env.Store,
		items: faultItems{ItemStore: env.Store.Items(), list: true},
	}
	if err = (Handler{}).Deployed(t.Context(), broken, c, good); err != nil {
		t.Fatal(err)
	}
	if err = (Handler{}).Deployed(t.Context(), env, c, bad); err == nil {
		t.Fatal("bad tarball deployed")
	}
}

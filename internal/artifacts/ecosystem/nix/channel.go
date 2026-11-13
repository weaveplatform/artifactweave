package nix

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

const (
	// ChannelFile is the one file a channel snapshot holds. The JFrog
	// documentation calls the name "mandatory and enforced" because
	// nix-channel --update asks for it under the URL its channel redirects
	// to.
	ChannelFile = "nixexprs.tar.xz"
	// MaxChannelBytes is the largest snapshot a channel's tarball may
	// expand to, 2 GiB; the compressed tarball shares MaxArchiveBytes.
	MaxChannelBytes = MaxNarBytes
)

// channelSegment is a channel name or snapshot identifier. NixOS's own,
// such as nixos-25.05-small or nixpkgs-darwin-25.05pre813816.3e3f3c7f9977,
// fit; a segment that starts with a dot cannot name . or .. or hide in a
// listing.
var channelSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,199}$`)

// Snapshot is one published snapshot of a channel: the channel's name, the
// flavor (nixpkgs or nixos) and the snapshot's identifier, the three
// segments a channel upload's path names after channels/.
type Snapshot struct {
	Channel, Flavor, ID string
}

// Dir is the folder that holds s: channels/<channel>/<flavor>/<id>, the
// path a client uploads s to and the URL a channel redirects to.
func (s Snapshot) Dir() string {
	return "channels/" + s.Channel + "/" + s.Flavor + "/" + s.ID
}

// Path is where s's tarball is stored, Dir followed by ChannelFile, which is
// the URL nix-channel --update downloads.
func (s Snapshot) Path() string { return s.Dir() + "/" + ChannelFile }

// ValidChannel reports whether name can name a channel.
func ValidChannel(name string) bool { return channelSegment.MatchString(name) }

// ValidFlavor reports whether flavor is one the JFrog documentation lists
// for a channel upload, nixpkgs or nixos: the first segment of the
// releases.nixos.org URL an official channel redirects to.
func ValidFlavor(flavor string) bool { return flavor == "nixpkgs" || flavor == "nixos" }

// ParseSnapshotDir returns the snapshot whose folder, Dir, is dir.
func ParseSnapshotDir(dir string) (Snapshot, bool) {
	parts := strings.Split(dir, "/")
	if len(parts) != 4 || parts[0] != "channels" {
		return Snapshot{}, false
	}
	s := Snapshot{Channel: parts[1], Flavor: parts[2], ID: parts[3]}
	if !ValidChannel(s.Channel) || !ValidFlavor(s.Flavor) || !channelSegment.MatchString(s.ID) {
		return Snapshot{}, false
	}
	return s, true
}

// ParseSnapshotPath returns the snapshot whose tarball, Path, is path.
func ParseSnapshotPath(path string) (Snapshot, bool) {
	dir, ok := strings.CutSuffix(path, "/"+ChannelFile)
	if !ok {
		return Snapshot{}, false
	}
	return ParseSnapshotDir(dir)
}

// IsChannelPath reports whether path is a channel snapshot's tarball.
func IsChannelPath(path string) bool {
	_, ok := ParseSnapshotPath(path)
	return ok
}

// ReadChannel checks that input, exactly size bytes, is a whole xz file,
// every stream ending in its index and footer, holding one tar archive and
// nothing more, as nix-channel unpacks it: the documentation requires
// nixexprs.tar.xz, so a gzip or bzip2 tarball, or an xz stream of anything
// else, is refused. The tarball must have an entry,
// stay within MaxChannelBytes expanded, and size must be 1 to
// MaxArchiveBytes; its block headers may name no dictionary above
// xzfile.MaxDict. Every refusal is ErrInvalid. ctx ends a wait for the xz
// dictionary budget, which is then a refusal too.
func ReadChannel(ctx context.Context, input io.Reader, size int64) error {
	if size <= 0 || size > MaxArchiveBytes {
		return ErrInvalid
	}
	compressed := &io.LimitedReader{R: input, N: size + 1}
	r, err := xzfile.NewReader(ctx, compressed, MaxChannelBytes+1)
	if err != nil {
		return ErrInvalid
	}
	defer func() { _ = r.Close() }()
	expanded := &io.LimitedReader{R: r, N: MaxChannelBytes + 1}
	archive := tar.NewReader(expanded)
	entries := 0
	for {
		_, err = archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalid
		}
		entries++
	}
	// The tar reader stops at the end-of-archive blocks. What follows them
	// must be the zero padding tar writers add, and the xz stream must end
	// where the upload does, so nothing rides behind the archive.
	if !zeros(expanded) || expanded.N == 0 || compressed.N != 1 || entries == 0 {
		return ErrInvalid
	}
	return nil
}

// zeros reports whether r holds nothing but zero bytes up to its end.
func zeros(r io.Reader) bool {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

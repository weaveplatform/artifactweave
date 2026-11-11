package vagrant

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/archivecheck"
)

const (
	// MaxArchiveBytes bounds a box at 16 GiB compressed, and its content at
	// 64 GiB expanded; a box's VM disk is streamed through the checks, never
	// held in memory.
	MaxArchiveBytes  int64 = 16 << 30
	maxExpandedBytes int64 = 64 << 30
	maxMetadataBytes       = 64 << 10
	maxEntries             = 100000
	// PropName is the property that names a box, <name> or
	// <namespace>/<name>. The items keep this name, PropProvider's,
	// PropVersion's and the derived vagrant.* names below, so they never
	// change.
	PropName = "box_name"
	// PropProvider is the property that names a box's provider, which its
	// metadata.json must confirm.
	PropProvider = "box_provider"
	// PropVersion is the property that holds a box's version.
	PropVersion      = "box_version"
	propProvider     = "vagrant.provider"
	propArchitecture = "vagrant.architecture"
	propDigest       = "vagrant.sha256"
)

var (
	component      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	versionPattern = regexp.MustCompile(
		`^[0-9]+(\.[0-9A-Za-z]+)*(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`,
	)
	errInvalid = errors.New("invalid Vagrant box")
)

type (
	// Identity is a box's identity as its properties give it: the box_name,
	// box_provider and box_version a client deploys it with.
	Identity struct{ Name, Provider, Version string }
	// Box is what a box's root metadata.json says of it: the provider it
	// runs on and, optionally, its architecture.
	Box struct {
		Provider     string `json:"provider"`
		Architecture string `json:"architecture,omitempty"`
	}
)

// ValidComponent reports whether s is one part of a box name, or a
// provider or architecture: at most 128 letters, digits, dots, underscores
// and hyphens, starting with a letter or digit.
func ValidComponent(s string) bool { return len(s) <= 128 && component.MatchString(s) }

// ValidName reports whether s is a box name: one valid component, or two
// joined by a slash (<namespace>/<name>).
func ValidName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !ValidComponent(p) {
			return false
		}
	}
	return true
}

// Properties returns the identity p gives a box and reports whether it is
// complete and valid: exactly one value each for box_name, box_provider and
// box_version, a valid name (ValidName) and provider (ValidComponent), and
// a dotted version of at most 128 bytes with an optional hyphenated
// pre-release.
func Properties(p model.Properties) (Identity, bool) {
	var id Identity
	for key, dst := range map[string]*string{PropName: &id.Name, PropProvider: &id.Provider, PropVersion: &id.Version} {
		if len(p[key]) != 1 {
			return id, false
		}
		*dst = p[key][0]
	}
	return id, ValidName(id.Name) && ValidComponent(id.Provider) && len(id.Version) <= 128 &&
		versionPattern.MatchString(id.Version)
}

// Verified returns the identity and the recorded box that p gives a stored
// box whose content has the SHA-256 sha, and reports whether they agree:
// the identity is valid (Properties), the recorded vagrant.sha256 is sha,
// the recorded vagrant.provider is box_provider, and any recorded
// architecture is valid. The index and the catalog take a box only when
// they do.
func Verified(p model.Properties, sha string) (Identity, Box, bool) {
	id, ok := Properties(p)
	b := Box{Provider: single(p[propProvider]), Architecture: single(p[propArchitecture])}
	return id, b, ok && sha != "" && single(p[propDigest]) == sha && b.Provider == id.Provider &&
		(b.Architecture == "" || ValidComponent(b.Architecture))
}

func single(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	return ""
}
func invalid(detail string) error { return fmt.Errorf("%w: %s", errInvalid, detail) }

// ReadBox validates every archive member and checksum without extracting any
// paths or executing the embedded Vagrantfile. ZIP files are spooled to a
// private temporary file so a VM image never needs to fit in memory.
func ReadBox(ctx context.Context, r io.Reader, size int64) (Box, error) {
	if size <= 0 || size > MaxArchiveBytes {
		return Box{}, invalid("archive size exceeds limit")
	}
	br := bufio.NewReader(io.LimitReader(contextReader{ctx: ctx, r: r}, size+1))
	magic, err := br.Peek(4)
	if err != nil {
		return Box{}, fmt.Errorf("vagrant truncated archive: %w", err)
	}
	switch {
	case string(magic) == "PK\x03\x04" || string(magic) == "PK\x05\x06":
		return readZIP(ctx, br, size)
	case magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return Box{}, fmt.Errorf("vagrant gzip: %w", err)
		}
		defer gz.Close()
		return readTar(ctx, gz, maxExpandedBytes)
	default:
		return readTar(ctx, br, maxExpandedBytes)
	}
}

// Keep cancellation responsive while copying VM disks or spooling ZIP data.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("vagrant archive canceled: %w", err)
	}
	n, err := r.r.Read(p)
	if err == io.EOF {
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("vagrant archive read: %w", err)
	}
	return n, nil
}

type archiveState struct {
	seen     map[string]bool
	entries  int
	total    int64
	metadata []byte
}

func (a *archiveState) member(name string, size int64, directory bool) (string, error) {
	if a.entries >= maxEntries {
		return "", invalid("too many archive entries")
	}
	a.entries++
	name = strings.TrimSuffix(name, "/")
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	if directory && name == "." {
		return "", nil
	}
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\\x00:") ||
		strings.HasPrefix(name, "/") ||
		path.Clean(name) != name ||
		strings.HasPrefix(name, "../") ||
		name == ".." ||
		size < 0 {
		return "", invalid("unsafe archive path")
	}
	if a.seen[name] || len(a.seen) >= maxEntries || size > maxExpandedBytes-a.total {
		return "", invalid("duplicate or oversized archive")
	}
	a.seen[name] = true
	a.total += size
	return name, nil
}

func (a *archiveState) read(ctx context.Context, r io.Reader, name string, size int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("vagrant archive canceled: %w", err)
	}
	r = contextReader{ctx: ctx, r: r}
	if name == "metadata.json" {
		if size > maxMetadataBytes {
			return invalid("metadata too large")
		}
		data, err := io.ReadAll(io.LimitReader(r, maxMetadataBytes+1))
		if err != nil || int64(len(data)) != size {
			return invalid("truncated metadata")
		}
		a.metadata = data
		return nil
	}
	if _, err := io.CopyN(io.Discard, r, size); err != nil {
		return fmt.Errorf("vagrant archive member: %w", err)
	}
	return nil
}

func (a *archiveState) box() (Box, error) {
	var b Box
	if json.Unmarshal(a.metadata, &b) != nil || !ValidComponent(b.Provider) ||
		(b.Architecture != "" && !ValidComponent(b.Architecture)) {
		return Box{}, invalid("missing or malformed root metadata.json")
	}
	return b, nil
}

func readTar(ctx context.Context, r io.Reader, limit int64) (Box, error) {
	// tar.Reader consumes extension records internally. Count the whole expanded
	// stream so hidden PAX/GNU headers cannot bypass the per-member limits.
	bounded := &io.LimitedReader{R: r, N: limit + 1}
	tr := tar.NewReader(bounded)
	a := archiveState{seen: map[string]bool{}}
	for {
		if err := ctx.Err(); err != nil {
			return Box{}, fmt.Errorf("vagrant archive canceled: %w", err)
		}
		h, err := tr.Next()
		if bounded.N == 0 {
			return Box{}, invalid("expanded archive exceeds limit")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Box{}, fmt.Errorf("vagrant tar: %w", err)
		}
		dir := h.Typeflag == tar.TypeDir
		if !dir && h.Typeflag != tar.TypeReg {
			return Box{}, invalid("archive links and special files are forbidden")
		}
		name, err := a.member(h.Name, h.Size, dir)
		if err != nil {
			return Box{}, err
		}
		if !dir {
			if err := a.read(ctx, tr, name, h.Size); err != nil {
				if bounded.N == 0 {
					return Box{}, invalid("expanded archive exceeds limit")
				}
				return Box{}, err
			}
		}
	}
	// Validate gzip trailers and reject appended non-padding data or streams.
	tail, err := io.ReadAll(io.LimitReader(bounded, 1<<20))
	if bounded.N == 0 {
		return Box{}, invalid("expanded archive exceeds limit")
	}
	if err != nil || len(tail) == 1<<20 || len(strings.Trim(string(tail), "\x00")) != 0 {
		return Box{}, invalid("invalid archive trailer")
	}
	return a.box()
}

func readZIP(ctx context.Context, r io.Reader, size int64) (Box, error) {
	f, err := os.CreateTemp("", "artifactweave-vagrant-*.zip")
	if err != nil {
		return Box{}, fmt.Errorf("vagrant temporary archive: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil || n != size {
		return Box{}, invalid("truncated ZIP archive")
	}
	z, err := archivecheck.ZIP(ctx, f, size, archivecheck.ZIPLimits{
		ArchiveBytes: MaxArchiveBytes, DirectoryBytes: 32 << 20, Entries: maxEntries,
	})
	if err != nil {
		return Box{}, fmt.Errorf("vagrant zip: %w", err)
	}
	a := archiveState{seen: map[string]bool{}}
	for _, entry := range z.File {
		if entry.UncompressedSize64 > uint64(maxExpandedBytes) {
			return Box{}, invalid("oversized ZIP entry")
		}
		dir := entry.FileInfo().IsDir()
		if !dir && !entry.Mode().IsRegular() {
			return Box{}, invalid("ZIP links and special files are forbidden")
		}
		name, err := a.member(entry.Name, int64(entry.UncompressedSize64), dir)
		if err != nil {
			return Box{}, err
		}
		rc, err := entry.Open()
		if err != nil {
			return Box{}, fmt.Errorf("vagrant ZIP entry: %w", err)
		}
		err = a.read(ctx, rc, name, int64(entry.UncompressedSize64))
		if err == nil {
			var extra [1]byte
			n, last := rc.Read(extra[:])
			if n != 0 || !errors.Is(last, io.EOF) {
				err = invalid("invalid ZIP checksum")
			}
		}
		_ = rc.Close()
		if err != nil {
			return Box{}, err
		}
	}
	return a.box()
}

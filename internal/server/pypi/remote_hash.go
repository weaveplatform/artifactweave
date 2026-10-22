package pypi

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // PyPI legacy distribution metadata
	"crypto/sha1" //nolint:gosec // PyPI legacy distribution metadata
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"hash"
	"io"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/blake2b"

	"github.com/weaveplatform/artifactweave/internal/archive"
	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var hashNames = []string{"sha512", "sha384", "sha256", "sha224", "blake2b_256", "sha1", "md5"}

func hashFor(name string) hash.Hash {
	switch name {
	case "sha512":
		return sha512.New()
	case "sha384":
		return sha512.New384()
	case "sha256":
		return sha256.New()
	case "sha224":
		return sha256.New224()
	case "blake2b_256":
		h, _ := blake2b.New256(nil)
		return h
	case "sha1":
		return sha1.New() //nolint:gosec // PyPI legacy distribution metadata
	case "md5":
		return md5.New() //nolint:gosec // PyPI legacy distribution metadata
	}
	return nil
}

func validateHashes(hashes map[string]string) error {
	for k, v := range hashes {
		h := hashFor(k)
		b, err := hex.DecodeString(v)
		if h == nil || err != nil || len(b) != h.Size() {
			return remoteError()
		}
	}
	return nil
}

func checksumPins(hashes map[string]string) types.Checksums {
	return types.Checksums{
		MD5:    strings.ToLower(hashes["md5"]),
		SHA1:   strings.ToLower(hashes["sha1"]),
		SHA256: strings.ToLower(hashes["sha256"]),
		SHA512: strings.ToLower(hashes["sha512"]),
	}
}

func verifyHashes(r io.Reader, hashes map[string]string) error {
	var writers []io.Writer
	actual := map[string]hash.Hash{}
	for k := range hashes {
		h := hashFor(k)
		if h == nil {
			return remoteError()
		}
		writers = append(writers, h)
		actual[k] = h
	}
	if _, err := io.Copy(io.MultiWriter(writers...), r); err != nil {
		return remoteError()
	}
	for k, h := range actual {
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), hashes[k]) {
			return remoteError()
		}
	}
	return nil
}

func (f remoteFile) validateArtifact(ctx context.Context, r io.ReadSeeker, size int64) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := verifyHashes(archiveReader{ctx: ctx, r: r}, f.hashes); err != nil {
		return err
	}
	ra := &archiveReaderAt{ctx: ctx, r: archive.ReaderAt(r)}
	if err := validateCompleteArchive(
		ctx,
		ra,
		size,
		f.name,
		remoteArchiveEntries,
		remoteArchiveInflated,
	); err != nil {
		return err
	}
	d, err := pypifmt.ReadDist(ra, size, f.name)
	if err != nil || pypifmt.Normalize(d.Name) != strings.Split(f.path, "/")[0] ||
		pypifmt.NormalizeVersion(d.Version) != pypifmt.NormalizeVersion(f.version) {
		return remoteError()
	}
	return nil
}

func (h handler) checkRemoteHashes(
	ctx context.Context,
	result source.Result,
	hashes map[string]string,
) error {
	if result.Item == nil {
		return nil
	}
	needsRead := false
	for k, v := range hashes {
		var got string
		switch k {
		case "md5":
			got = result.Item.Actual.MD5
		case "sha1":
			got = result.Item.Actual.SHA1
		case "sha256":
			got = result.Item.Actual.SHA256
		case "sha512":
			got = result.Item.Actual.SHA512
		default:
			needsRead = true
			continue
		}
		if !strings.EqualFold(got, v) {
			return remoteError()
		}
	}
	if !needsRead {
		return nil
	}
	r, _, err := h.k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return remoteError()
	}
	defer r.Close()
	return verifyHashes(r, hashes)
}

func (f remoteFile) validateCore(r io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(r, pypifmt.MaxMetadata+1))
	if err != nil || len(b) > pypifmt.MaxMetadata || !utf8.Valid(b) {
		return remoteError()
	}
	if err = verifyHashes(bytes.NewReader(b), f.metadata); err != nil {
		return err
	}
	m, err := mail.ReadMessage(bytes.NewReader(b))
	if err != nil {
		return remoteError()
	}
	if len(m.Header["Name"]) != 1 || len(m.Header["Version"]) != 1 ||
		pypifmt.Normalize(m.Header.Get("Name")) != strings.Split(f.path, "/")[0] ||
		pypifmt.NormalizeVersion(m.Header.Get("Version")) != pypifmt.NormalizeVersion(f.version) {
		return remoteError()
	}
	return nil
}

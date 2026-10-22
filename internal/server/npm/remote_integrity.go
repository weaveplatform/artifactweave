package npm

import (
	"context"
	"crypto/sha1" //nolint:gosec // retained npm SHA-1 integrity compatibility
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type integritySpec struct {
	algorithm string
	digests   []string
}

// npm's ssri selects the strongest supported algorithm, then accepts any one
// digest at that strength. Weaker matches cannot rescue a stronger mismatch.
func parseIntegrity(raw string) (integritySpec, error) {
	var spec integritySpec
	if raw == "" {
		return spec, nil
	}
	strength := 0
	for _, token := range strings.Fields(raw) {
		algorithm, value, ok := strings.Cut(token, "-")
		if !ok {
			return spec, remoteProtocolError()
		}
		rank, size := integrityAlgorithm(algorithm)
		if rank == 0 {
			continue
		}
		value, _, _ = strings.Cut(value, "?")
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(value)
		}
		if err != nil || len(decoded) != size {
			return spec, remoteProtocolError()
		}
		if rank > strength {
			strength = rank
			spec = integritySpec{algorithm: algorithm}
		}
		if rank == strength {
			digest := hex.EncodeToString(decoded)
			if !slices.Contains(spec.digests, digest) {
				spec.digests = append(spec.digests, digest)
			}
		}
	}
	if strength == 0 {
		return spec, remoteProtocolError()
	}
	return spec, nil
}

func integrityAlgorithm(name string) (rank, size int) {
	switch name {
	case "sha512":
		return 4, 64
	case "sha384":
		return 3, 48
	case "sha256":
		return 2, 32
	case "sha1":
		return 1, 20
	default:
		return 0, 0
	}
}

func distChecksums(integrity, shasum string) (types.Checksums, error) {
	sums, err := item.ParseChecksums(types.Checksums{SHA1: shasum})
	if err != nil {
		return types.Checksums{}, remoteProtocolError()
	}
	spec, err := parseIntegrity(integrity)
	if err != nil {
		return types.Checksums{}, err
	}
	if len(spec.digests) == 1 {
		switch spec.algorithm {
		case "sha512":
			sums.SHA512 = spec.digests[0]
		case "sha256":
			sums.SHA256 = spec.digests[0]
		case "sha1":
			if sums.SHA1 != "" && sums.SHA1 != spec.digests[0] {
				return types.Checksums{}, remoteProtocolError()
			}
			sums.SHA1 = spec.digests[0]
		}
	}
	return sums, nil
}

func (s integritySpec) validate(_ context.Context, r io.ReadSeeker, _ int64) error {
	return s.verify(r)
}

func (s integritySpec) verify(r io.Reader) error {
	var digest hash.Hash
	switch s.algorithm {
	case "sha1":
		digest = sha1.New() //nolint:gosec // retained npm SHA-1 integrity compatibility
	case "sha256":
		digest = sha256.New()
	case "sha384":
		digest = sha512.New384()
	default:
		digest = sha512.New()
	}
	if _, err := io.Copy(digest, r); err != nil {
		return remoteProtocolError()
	}
	if !slices.Contains(s.digests, hex.EncodeToString(digest.Sum(nil))) {
		return remoteProtocolError()
	}
	return nil
}

func (s integritySpec) checkResult(
	ctx context.Context,
	k *pkgroute.Kit,
	result source.Result,
) error {
	if s.algorithm == "" || result.Item == nil {
		return nil
	}
	// Offline reads deliberately bypass representation keys. Recheck their
	// selected SRI too; SHA-384 is not a persisted checksum column.
	var digest string
	switch s.algorithm {
	case "sha512":
		digest = result.Item.Actual.SHA512
	case "sha256":
		digest = result.Item.Actual.SHA256
	case "sha1":
		digest = result.Item.Actual.SHA1
	}
	if s.algorithm != "sha384" {
		if !slices.Contains(s.digests, digest) {
			return remoteProtocolError()
		}
		return nil
	}
	body, _, err := k.Blobs().Open(ctx, result.Item.Actual.SHA256)
	if err != nil {
		return remoteProtocolError()
	}
	defer body.Close()
	return s.verify(body)
}

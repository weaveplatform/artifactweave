package huggingface

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/huggingface/xet"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type xetWriteKey struct{}

// XetWrite marks a validated CAS publication; HTTP properties cannot opt in.
func XetWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, xetWriteKey{}, true)
}

// IsXetPath reports whether path lies in an identity's private .xet folder,
// whatever follows it, so the hooks refuse every generic access to it, a
// malformed record path included.
func IsXetPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) >= 2 && parts[1] == ".xet"
}

// ValidXetPath reports whether path names one Xet record,
// <prefix>/.xet/<kind>/<hash>: a prefix whose identity ParseIdentity
// accepts, a kind of xorbs, files, sha256 or stages, and a hash in Xet's
// textual form (xet.ParseHash).
func ValidXetPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[1] != ".xet" ||
		parts[2] != "xorbs" && parts[2] != "files" && parts[2] != "sha256" && parts[2] != "stages" {
		return false
	}
	identity := strings.SplitN(parts[0], "--", 3)
	if len(identity) != 3 {
		return false
	}
	_, ok := ParseIdentity(identity[0], identity[1], identity[2])
	_, valid := xet.ParseHash(parts[3])
	return ok && valid
}

// GuardTransferSource refuses, with 403, a copy or move whose source is a
// Xet record. Raw CAS records are read only through the native endpoint,
// whose capabilities recheck the caller (docs/testing/huggingface.md).
func (Handler) GuardTransferSource(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	it types.Item,
) error {
	if IsXetPath(it.FullPath()) {
		return format.Rejectf(
			http.StatusForbidden,
			"Xet records must be accessed through the native model endpoint",
		)
	}
	return nil
}

// Resolve refuses, with 403, a generic download of a Xet record, for the
// same reason as GuardTransferSource, and resolves any other path to
// itself.
func (Handler) Resolve(
	_ context.Context,
	_ format.Env,
	_ repo.Config,
	d format.Download,
) (string, error) {
	if IsXetPath(d.Path) {
		return "", format.Rejectf(
			http.StatusForbidden,
			"Xet records must be accessed through the native model endpoint",
		)
	}
	return d.Path, nil
}

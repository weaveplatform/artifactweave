package huggingface

import (
	"context"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface/xet"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type xetWriteKey struct{}

// XetWrite marks a validated CAS publication; HTTP properties cannot opt in.
func XetWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, xetWriteKey{}, true)
}

func IsXetPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) >= 2 && parts[1] == ".xet"
}

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

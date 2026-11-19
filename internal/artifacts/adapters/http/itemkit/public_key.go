package itemkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

var errPublicKeyTooLarge = errors.New("RPM public key exceeds management body limit")

func rpmPublicKeyArtifact(f Found) bool {
	path := f.Item.FullPath()
	return f.Repo.PackageType == repo.RPM && !f.Item.Cached &&
		(f.Repo.Class == repo.Local || f.Repo.Class == repo.Federated) &&
		(path == "repodata/repomd.xml.key" || strings.HasSuffix(path, "/repodata/repomd.xml.key"))
}

// publicKeyContent guards historical RPM artifacts generated from a contaminated
// public field. Read/policy already selected f; validate and serve one opened SHA
// so a later item replacement cannot swap the checked payload. Remote files and
// ordinary artifacts retain their normal streaming path.
func (s *Kit) publicKeyContent(ctx context.Context, f Found) (io.ReadSeekCloser, error) {
	content, info, err := s.Blobs.Open(ctx, f.Item.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("open RPM public key: %w", err)
	}
	if info.Size > middleware.ManagementBodyLimit {
		_ = content.Close()
		return nil, errPublicKeyTooLarge
	}
	// A public field admitted through the management API fits this existing
	// bound; an out-of-band artifact must not force an unbounded allocation.
	body, err := io.ReadAll(io.LimitReader(content, middleware.ManagementBodyLimit+1))
	if err == nil && len(body) > middleware.ManagementBodyLimit {
		err = errPublicKeyTooLarge
	}
	if err == nil {
		err = keymaterial.CheckPublic(keymaterial.GPG, string(body))
	}
	if err == nil {
		_, err = content.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = content.Close()
		return nil, fmt.Errorf("read RPM public key: %w", err)
	}
	return content, nil
}

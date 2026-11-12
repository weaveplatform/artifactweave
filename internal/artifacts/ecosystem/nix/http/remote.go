package nixhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
)

func (h handler) verifyRemote(r *http.Request, info nixfmt.Info) error {
	c := pkgroute.RequestFrom(r).Repo
	refs := []string{c.PrimaryKeyPairRef, c.SecondaryKeyPairRef}
	configured := false
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		configured = true
		pair, err := h.o.KeyPairs.Keypair(r.Context(), ref)
		if err != nil || pair.Type != api.KeypairEd25519 {
			continue
		}
		pub, err := keymaterial.Ed25519PublicKey(pair.PublicKey)
		if err == nil && info.Verify(pair.Alias, pub) {
			return nil
		}
	}
	if configured {
		return source.ErrInvalidResult
	}
	// Without a configured trust root, retain the upstream signature for the
	// native client's trusted-public-keys check; do not claim server verification.
	return nil
}

func checkURL(c repo.Config, target *url.URL) error {
	base, err := url.Parse(c.URL)
	if err != nil || target == nil || target.Scheme != base.Scheme || target.Host != base.Host ||
		target.User != nil ||
		target.Fragment != "" ||
		target.RawQuery != "" || path.Clean(target.Path) != target.Path || strings.ContainsAny(target.Path, "\\\x00") ||
		!strings.HasPrefix(target.Path, strings.TrimRight(base.Path, "/")+"/") {
		return source.ErrInvalidResult
	}
	return nil
}

// remoteInfo reads a remote's narinfo at path, for a narinfo request or,
// when nar is set, for a request of the NAR it names. The read is a lookup
// that only the blackout refuses, its policy being checked at the root:
// lookup has checked the remote's policy at the path the client asked for.
func (h handler) remoteInfo(r *http.Request, path string, nar bool) (located, error) {
	c := pkgroute.RequestFrom(r).Repo
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		PermissionFolder:  true,
		PermissionPath:    "",
		Path:              path,
		ExternalURL:       strings.TrimRight(c.URL, "/") + "/" + path,
		Method:            http.MethodGet,
		Variant:           "nix-narinfo-v1",
		NoCrossOriginAuth: true,
		CheckURL:          func(_ context.Context, target *url.URL) error { return checkURL(c, target) },
		ValidateMetadata: func(_ context.Context, body io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
			data, err := io.ReadAll(io.LimitReader(body, nixfmt.MaxMetadataBytes+1))
			if err != nil {
				return source.ErrInvalidResult
			}
			info, err := nixfmt.Parse(data, path)
			if err != nil {
				return source.ErrInvalidResult
			}
			return h.verifyRemote(r, info)
		},
	}
	result, err := h.k.Read(r, resource)
	if err != nil {
		return located{}, fmt.Errorf("nix: remote metadata: %w", err)
	}
	var reader io.ReadCloser
	switch {
	case result.Stream != nil:
		reader = result.Stream.Body
	case result.Item != nil:
		rc, _, err := h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
		if err != nil {
			return located{}, fmt.Errorf("nix: metadata blob: %w", err)
		}
		reader = rc
	default:
		return located{}, source.ErrInvalidResult
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, nixfmt.MaxMetadataBytes+1))
	if err != nil {
		return located{}, source.ErrInvalidResult
	}
	info, err := nixfmt.Parse(data, path)
	if err != nil {
		return located{}, source.ErrInvalidResult
	}
	if err = h.verifyRemote(r, info); err != nil {
		return located{}, err
	}
	if result.Revalidate == nil || result.CheckRead == nil || result.CheckPermission == nil {
		return located{}, source.ErrInvalidResult
	}
	// The fence rechecks the policy at the path the client asked for and
	// Read permission alone at the other: patterns rejecting only the NAR
	// leave the narinfo served, where CheckRead's refusal of the NAR made
	// the narinfo a 502 that turns the Nix client's substituter off
	// (R1, R3), and a NAR request is not refused at the narinfo it
	// was found through (R4).
	checkInfo, checkNAR := result.CheckRead, result.CheckPermission
	if nar {
		checkInfo, checkNAR = result.CheckPermission, result.CheckRead
	}
	check := func(ctx context.Context) error {
		if err := result.Revalidate(ctx); err != nil {
			return fmt.Errorf("nix: metadata authority: %w", err)
		}
		if err := checkInfo(ctx, path); err != nil {
			return fmt.Errorf("nix: metadata read authority: %w", err)
		}
		return checkNAR(ctx, info.URL)
	}
	if err = check(r.Context()); err != nil {
		return located{}, err
	}
	return located{info: info, r: r, check: check}, nil
}

func (h handler) remoteArchive(w http.ResponseWriter, r *http.Request, found located) {
	info := found.info
	c := pkgroute.RequestFrom(found.r).Repo
	if err := found.check(r.Context()); err != nil {
		h.fail(w, r, err)
		return
	}
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              info.URL,
		ExternalURL:       strings.TrimRight(c.URL, "/") + "/" + info.URL,
		Method:            http.MethodGet,
		Variant:           "nix-nar-v1",
		Checksums:         kernel.Checksums{SHA256: info.FileHash},
		NoCrossOriginAuth: true,
		CheckURL:          func(_ context.Context, target *url.URL) error { return checkURL(c, target) },
		Validate: func(_ context.Context, body io.ReadSeeker, size int64) error {
			a, err := nixfmt.ReadArchive(body, size, info.Compression)
			if err != nil || !a.Matches(info) {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.Read(found.r, resource)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	previous := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		if err := found.check(ctx); err != nil {
			return err
		}
		if previous == nil {
			return source.ErrInvalidResult
		}
		return previous(ctx)
	}
	h.k.ServeResult(w, found.r, result)
}

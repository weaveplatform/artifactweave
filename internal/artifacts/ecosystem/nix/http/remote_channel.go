package nixhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// A remote repository resolves channels at the NixOS Channel Registry
// (Options.Channels), whatever its own URL: the JFrog documentation says
// its channel name "must be an exact match for an official name in the
// NixOS Channel Registry" (channels.nixos.org), while the remote's URL is
// the binary cache (https://cache.nixos.org in JFrog's examples) [I].

// below reports whether target is a plain URL beneath base: same scheme
// and host, no credentials, query or fragment, and a clean path under
// base's, as checkURL holds a binary cache's reads beneath its origin.
func below(base, target *url.URL) bool {
	return target != nil && target.Scheme == base.Scheme && target.Host == base.Host &&
		target.User == nil && target.Fragment == "" && target.RawQuery == "" &&
		path.Clean(target.Path) == target.Path && !strings.ContainsAny(target.Path, "\\\x00") &&
		strings.HasPrefix(target.Path, strings.TrimRight(base.Path, "/")+"/")
}

// release returns the snapshot of channel name that a release URL, as the
// registry redirects to it, names: <releases>/<flavor>/[<series>/]<id>.
func release(releases *url.URL, name, raw string) (nixfmt.Snapshot, bool) {
	u, err := url.Parse(raw)
	if err != nil || !below(releases, u) {
		return nixfmt.Snapshot{}, false
	}
	parts := strings.Split(
		strings.TrimPrefix(u.Path, strings.TrimRight(releases.Path, "/")+"/"),
		"/",
	)
	if len(parts) < 2 || len(parts) > 3 {
		return nixfmt.Snapshot{}, false
	}
	return nixfmt.ParseSnapshotDir("channels/" + name + "/" + parts[0] + "/" + parts[len(parts)-1])
}

// remoteChannel reads where the channel registry sends channel name now,
// through the remote's source policy, so its cache period, offline mode
// and proxy apply and the registry's answer is cached at channels/<name>.
// It returns that snapshot and its release URL. Only the registry and the
// releases origin may be reached, and the remote's credentials are never
// sent there. The read is computed metadata, which only the blackout
// refuses (R2a).
func (h handler) remoteChannel(r *http.Request, name string) (nixfmt.Snapshot, string, error) {
	channels, releases := h.o.Channels, h.o.Releases
	folder := "channels/" + name
	resource := source.Resource{
		Kind:              source.ResourceKindMetadata,
		PermissionFolder:  true,
		PermissionPath:    folder,
		Path:              folder,
		ExternalURL:       channels.JoinPath(name).String(),
		Method:            http.MethodGet,
		Variant:           "nix-channel-v1",
		NoCrossOriginAuth: true,
		BlackoutOnly:      true,
		// The release a channel redirects to answers an HTML page, which
		// the remote's mismatching MIME type block would otherwise refuse.
		ExpectedContentType: "text/html",
		CheckURL: func(_ context.Context, target *url.URL) error {
			if below(channels, target) || below(releases, target) {
				return nil
			}
			return source.ErrInvalidResult
		},
		ValidateMetadata: func(_ context.Context, _ io.ReadSeeker, _ int64, m source.MetadataResponse) error {
			if _, ok := release(releases, name, m.BaseURL); !ok {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := h.k.ReadMetadata(r, resource)
	if err != nil {
		return nixfmt.Snapshot{}, "", fmt.Errorf("nix: remote channel: %w", err)
	}
	if result.Stream != nil {
		_ = result.Stream.Body.Close()
	}
	s, ok := release(releases, name, result.Metadata.BaseURL)
	if !ok || result.Revalidate == nil {
		return nixfmt.Snapshot{}, "", source.ErrInvalidResult
	}
	if err = result.Revalidate(r.Context()); err != nil {
		return nixfmt.Snapshot{}, "", fmt.Errorf("nix: remote channel authority: %w", err)
	}
	return s, result.Metadata.BaseURL, nil
}

// remoteSnapshot reads snapshot s's tarball through a remote: Read
// permission first (DV-057), then the remote's policy at the tarball's
// path, before the registry is asked, so a refusal names the tarball;
// then the tarball the registry's current release holds, validated as an
// upload is and cached at s.Path(). A remote serves the snapshot its
// channel names now; another snapshot is not found [I], since the
// registry's URL for an older release cannot be rebuilt from its path.
func (h handler) remoteSnapshot(r *http.Request, s nixfmt.Snapshot) (source.Result, error) {
	if !h.k.Can(r, s.Path(), accessapi.ActionRead) {
		return source.Result{}, source.ErrDenied
	}
	if err := h.k.CheckDocument(r, s.Path()); err != nil {
		return source.Result{}, fmt.Errorf("nix: remote snapshot: %w", err)
	}
	current, base, err := h.remoteChannel(r, s.Channel)
	if err != nil {
		return source.Result{}, err
	}
	if current != s {
		return source.Result{}, kernel.ErrNotFound
	}
	releases := h.o.Releases
	target := base + "/" + nixfmt.ChannelFile
	result, err := h.k.Read(r, source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              s.Path(),
		ExternalURL:       target,
		Method:            http.MethodGet,
		Variant:           "nix-channel-tarball-v1",
		NoCrossOriginAuth: true,
		CheckURL: func(_ context.Context, u *url.URL) error {
			if below(releases, u) {
				return nil
			}
			return source.ErrInvalidResult
		},
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if nixfmt.ReadChannel(ctx, body, size) != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("nix: remote snapshot: %w", err)
	}
	return result, nil
}

package npmhttp

import (
	"context"
	"crypto/sha1" //nolint:gosec // npm's retained SHA-1 compatibility
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (h handler) virtualTarball(w http.ResponseWriter, r *http.Request, name, path string) {
	if !h.virtualFolder(w, r, name) {
		return
	}
	if err := h.k.CheckDownload(r, path); err != nil {
		h.k.Error(w, r, err)
		return
	}
	var model npmfmt.VirtualPackage
	var expected *npmfmt.VirtualVersion
	// A direct strict-layout file read need not have an indexed packument. A
	// readable advertised version, however, must retain its cached winning bytes
	// even when an earlier member gains a duplicate during the metadata TTL.
	if err := h.k.CheckDocument(r, documentPath(name)); err == nil {
		var err error
		model, err = h.virtualModel(r, name)
		if err != nil && !member404(err) && !pkgroute.IsRefusal(err) {
			h.k.Error(w, r, err)
			return
		}
		for number, version := range model.Versions {
			if npmfmt.CanonicalPath(name, number) == path {
				v := version
				expected = &v
				break
			}
		}
	} else if !pkgroute.IsRefusal(err) {
		h.k.Error(w, r, err)
		return
	}
	members, err := h.k.DownloadMembers(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, api.ActionRead) {
			continue
		}
		c := pkgroute.RequestFrom(mr).Repo
		if c.Class == repo.Remote {
			result, err := h.remoteTarballResult(mr, name, path)
			if err != nil {
				h.evictVirtual404(r, name, c.Key, model, err)
				if virtualPassedOver(err) {
					h.k.WarnPassedOver(mr, err)
					continue
				}
				h.k.Error(w, r, err)
				return
			}
			matches, err := h.matchesVirtual(r, expected, c.Key, result)
			if err != nil || !matches {
				if result.Stream != nil {
					_ = result.Stream.Body.Close()
				}
				if err != nil {
					h.k.Error(w, r, err)
					return
				}
				continue
			}
			h.fenceVirtualDelivery(r, name, c.Key, model, &result)
			h.k.ServeResult(w, mr, result)
			return
		}
		found, err := h.k.ResolveDownloadItem(mr, path)
		if err == nil && found.Item.Folder {
			err = kernel.ErrNotFound
		}
		if err != nil {
			h.evictVirtual404(r, name, c.Key, model, err)
			if virtualPassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			h.k.Error(w, r, err)
			return
		}
		matches, err := h.matchesVirtual(r, expected, c.Key, source.Result{Item: &found.Item})
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if !matches {
			continue
		}
		h.k.ServeFound(w, mr, found)
		return
	}
	h.k.Error(w, r, notFound(r))
}

func member404(err error) bool {
	if pkgroute.IsRefusal(err) {
		return false
	}
	var answer *apierror.Error
	return errors.Is(err, kernel.ErrNotFound) || errors.As(err, &answer) && answer.Status == http.StatusNotFound
}

func (h handler) evictVirtual404(r *http.Request, name, member string, model npmfmt.VirtualPackage, err error) {
	if member404(err) && slices.Contains(model.Contributors, member) {
		h.k.EvictMerged(r, documentPath(name))
	}
}

func (h handler) matchesVirtual(r *http.Request, expected *npmfmt.VirtualVersion, member string, result source.Result) (bool, error) {
	if expected == nil {
		return true, nil
	}
	var manifest struct {
		Dist struct {
			Shasum    string `json:"shasum"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(expected.Manifest, &manifest); err != nil {
		return false, fmt.Errorf("npm cached winning manifest: %w", err)
	}
	actual := kernel.Checksums{}
	if result.Item != nil {
		actual = result.Item.Actual
	} else if result.Stream != nil {
		actual = result.Stream.Checksums
	}
	declared, err := distChecksums(manifest.Dist.Integrity, manifest.Dist.Shasum)
	if err != nil {
		return false, err
	}
	if declared.SHA1 != "" && actual.SHA1 != declared.SHA1 {
		return false, nil
	}
	spec, err := parseIntegrity(manifest.Dist.Integrity)
	if err != nil {
		return false, err
	}
	if spec.algorithm == "" {
		return declared.SHA1 != "" || member == expected.Member, nil
	}
	digest := map[string]string{"sha1": actual.SHA1, "sha256": actual.SHA256, "sha512": actual.SHA512}[spec.algorithm]
	if digest != "" {
		return slices.Contains(spec.digests, digest), nil
	}
	// SHA-384 has no stored checksum column. Hash only the verified, seekable
	// server copy and restore a transient stream before serving it.
	var body io.ReadSeeker
	if result.Item != nil {
		opened, _, err := h.k.Blobs().Open(r.Context(), actual.SHA256)
		if err != nil {
			return false, fmt.Errorf("npm winning tarball open: %w", err)
		}
		defer opened.Close()
		body = opened
	} else if result.Stream != nil {
		body, _ = result.Stream.Body.(io.ReadSeeker)
	}
	if body == nil {
		return false, source.ErrInvalidResult
	}
	position, err := body.Seek(0, io.SeekCurrent)
	if err != nil {
		return false, fmt.Errorf("npm winning tarball position: %w", err)
	}
	defer func() { _, _ = body.Seek(position, io.SeekStart) }()
	var sum hash.Hash
	switch spec.algorithm {
	case "sha1":
		sum = sha1.New() //nolint:gosec // npm SRI compatibility
	case "sha256":
		sum = sha256.New()
	case "sha384":
		sum = sha512.New384()
	default:
		sum = sha512.New()
	}
	if _, err = io.Copy(sum, body); err != nil {
		return false, fmt.Errorf("npm winning tarball checksum: %w", err)
	}
	return slices.Contains(spec.digests, hex.EncodeToString(sum.Sum(nil))), nil
}

// The final source fence can observe a member disappearing or losing Read
// after resolution. Keep that member miss inside the virtual namespace; the
// direct remote adapter retains its own refusal and internal-fault behavior.
func (h handler) fenceVirtualDelivery(r *http.Request, name, member string, model npmfmt.VirtualPackage, result *source.Result) {
	if result.Revalidate == nil {
		return
	}
	original := result.Revalidate
	result.Revalidate = func(ctx context.Context) error {
		err := original(ctx)
		if virtualPassedOver(err) {
			h.evictVirtual404(r, name, member, model, err)
			return virtualNotFound(name)
		}
		return err
	}
}

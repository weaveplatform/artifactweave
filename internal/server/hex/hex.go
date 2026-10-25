// Package hex serves the signed Hex registry v2 protocol.
package hex

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/internal/keymaterial"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type Options struct{ RemoteSources *source.Service }

func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Virtual: virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name: "hex", Types: []repo.PackageType{repo.Hex},
		Surface: apierror.Artifactory, Sources: sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k}
			r.Get("/*", h.read)
			r.Head("/*", h.read)
		},
	}
}

type virtualSource struct{}

func (virtualSource) Item(
	_ context.Context,
	_ store.Store,
	_ repo.Config,
	_ string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

type handler struct{ k *pkgroute.Kit }

var errStoredPackageIdentity = errors.New("hex: stored package identity mismatch")

func (h handler) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	h.k.Error(w, r, apierror.New(status, msg))
}

func (h handler) signingKey(r *http.Request) (*rsa.PrivateKey, error) {
	c := pkgroute.RequestFrom(r).Repo
	ref := c.PrimaryKeyPairRef
	if c.Class == repo.Virtual && ref == "" {
		ref = c.KeyPair
	}
	if ref == "" {
		return nil, apierror.New(http.StatusServiceUnavailable, "Hex signing key is not configured")
	}
	pair, err := h.k.DB().Keys().Keypair(r.Context(), ref)
	if err != nil || pair.Type != types.KeypairRSA {
		return nil, apierror.New(
			http.StatusServiceUnavailable,
			"Hex RSA signing key is unavailable",
		)
	}
	key, err := keymaterial.RSAPrivateKey(pair.PrivateKey, pair.Passphrase)
	if err != nil || key.N.BitLen() < 2048 {
		return nil, apierror.New(http.StatusServiceUnavailable, "Hex RSA signing key is invalid")
	}
	return key, nil
}

func (h handler) read(w http.ResponseWriter, r *http.Request) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil || r.URL.RawQuery != "" {
		h.fail(w, r, http.StatusBadRequest, "invalid Hex path or query")
		return
	}
	path := p.String()
	if hexfmt.IsTarballPath(path) {
		h.tarball(w, r, path)
		return
	}
	if path != "public_key" && path != "names" && path != "versions" &&
		(!strings.HasPrefix(path, "packages/") || !hexfmt.ValidName(strings.TrimPrefix(path, "packages/"))) {
		h.fail(w, r, http.StatusNotFound, "Hex resource not found")
		return
	}
	if path == "public_key" {
		if !h.k.RequireFolder(w, r, "", types.ActionRead) {
			return
		}
		key, err := h.signingKey(r)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		h.write(w, r, []byte(hexfmt.PublicKeyPEM(key)), "application/x-pem-file")
		return
	}
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	packages, err := h.packages(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	key, err := h.signingKey(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var data []byte
	if strings.HasPrefix(path, "packages/") {
		name := strings.TrimPrefix(path, "packages/")
		pkg, ok := packages[name]
		if !ok {
			h.fail(w, r, http.StatusNotFound, "Hex package not found")
			return
		}
		data, err = hexfmt.EncodePackage(pkg, key)
	} else {
		all := make([]hexfmt.RegistryPackage, 0, len(packages))
		names := make([]string, 0, len(packages))
		for name, pkg := range packages {
			all = append(all, pkg)
			names = append(names, name)
		}
		if path == "names" {
			data, err = hexfmt.EncodeNames(pkgroute.RequestFrom(r).Repo.Key, names, key)
		} else {
			data, err = hexfmt.EncodeVersions(pkgroute.RequestFrom(r).Repo.Key, all, key)
		}
	}
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("hex: encode registry: %w", err))
		return
	}
	h.write(w, r, data, "application/octet-stream")
}

func (h handler) write(w http.ResponseWriter, r *http.Request, data []byte, mediaType string) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		// The fixed binary or PEM Content-Type prevents these bytes from being rendered as HTML.
		//nolint:gosec // G705 reports a false positive for this fixed Content-Type response.
		_, _ = w.Write(data)
	}
}

func (h handler) packages(r *http.Request, path string) (map[string]hexfmt.RegistryPackage, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Virtual {
		return h.virtualPackages(r, path)
	}
	if c.Class == repo.Remote {
		return h.remotePackages(r, path)
	}
	items, err := hexfmt.Files(r.Context(), h.k.DB(), c)
	if err != nil {
		return nil, fmt.Errorf("hex: list repository packages: %w", err)
	}
	out := map[string]hexfmt.RegistryPackage{}
	for _, it := range items {
		p := it.FullPath()
		if !hexfmt.IsTarballPath(p) || !h.k.Can(r, p, types.ActionRead) {
			continue
		}
		pkg, err := h.packageItem(r, it)
		if err != nil {
			return nil, err
		}
		if path != "names" && path != "versions" && path != "packages/"+pkg.Name {
			continue
		}
		entry := out[pkg.Name]
		entry.Name, entry.Repository = pkg.Name, c.Key
		entry.Releases = append(entry.Releases, pkg)
		out[pkg.Name] = entry
	}
	for name, entry := range out {
		slices.SortFunc(
			entry.Releases,
			func(a, b hexfmt.Package) int { return strings.Compare(a.Version, b.Version) },
		)
		out[name] = entry
	}
	return out, nil
}

func (h handler) packageItem(r *http.Request, it types.Item) (hexfmt.Package, error) {
	rc, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		return hexfmt.Package{}, fmt.Errorf("hex: open stored package: %w", err)
	}
	defer rc.Close()
	pkg, err := hexfmt.InspectTarball(rc)
	if err != nil {
		return pkg, fmt.Errorf("hex: invalid stored package: %w", err)
	}
	canonical, _ := hexfmt.TarballPath(pkg.Name, pkg.Version)
	if canonical != it.FullPath() {
		return pkg, errStoredPackageIdentity
	}
	return pkg, nil
}

func (h handler) tarball(w http.ResponseWriter, r *http.Request, path string) {
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Virtual {
		members, err := h.members(r)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		for _, mr := range members {
			if !h.k.Can(mr, path, types.ActionRead) {
				continue
			}
			if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
				// The remote adapter resolves and validates the artifact against
				// its upstream signed registry before delivery.
				result, err := h.remoteResult(mr, path)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					h.k.Error(w, r, pkgroute.SourceError(err, false))
					return
				}
				h.k.ServeResult(w, mr, result)
				return
			}
			found, err := h.k.Item(mr, path)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			h.k.ServeFound(w, mr, found)
			return
		}
		h.fail(w, r, http.StatusNotFound, "Hex tarball not found")
		return
	}
	if c.Class == repo.Remote {
		h.remoteTarball(w, r, path)
		return
	}
	h.k.ServeItem(w, r, path)
}

func (h handler) members(r *http.Request) ([]*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	seen := map[string]bool{c.Key: true}
	var out []*http.Request
	for _, key := range c.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid Hex virtual member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hex: load virtual member: %w", err)
		}
		if member.PackageType != repo.Hex ||
			member.Class != repo.Local && member.Class != repo.Remote {
			return nil, apierror.New(http.StatusBadRequest, "invalid Hex virtual member")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("hex: authorize virtual member: %w", err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) virtualPackages(
	r *http.Request,
	path string,
) (map[string]hexfmt.RegistryPackage, error) {
	members, err := h.members(r)
	if err != nil {
		return nil, err
	}
	out := map[string]hexfmt.RegistryPackage{}
	for _, mr := range members {
		entries, err := h.packages(mr, path)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for name, entry := range entries {
			current := out[name]
			if current.Name == "" {
				current.Name = name
			}
			current.Repository = pkgroute.RequestFrom(r).Repo.Key
			seen := map[string]bool{}
			for _, release := range current.Releases {
				seen[release.Version] = true
			}
			offset := uint64(len(current.Advisories))
			added := false
			for _, release := range entry.Releases {
				if !seen[release.Version] {
					mapped := rebindRelease(release, entry.Repository, current.Repository)
					mapped.AdvisoryIndexes = slices.Clone(mapped.AdvisoryIndexes)
					for i := range mapped.AdvisoryIndexes {
						mapped.AdvisoryIndexes[i] += offset
					}
					current.Releases = append(current.Releases, mapped)
					added = true
				}
			}
			if added {
				current.Advisories = append(current.Advisories, entry.Advisories...)
			}
			out[name] = current
		}
	}
	for name, entry := range out {
		out[name] = compactAdvisories(entry)
	}
	return out, nil
}

// A package proxied into this repository must keep same-registry dependencies
// inside the repository Mix selected. Explicit references to another registry
// retain their original destination.
func rebindRelease(release hexfmt.Package, from, to string) hexfmt.Package {
	if from == "" || from == to {
		return release
	}
	release.Requirements = slices.Clone(release.Requirements)
	for i := range release.Requirements {
		if release.Requirements[i].Repository == from {
			release.Requirements[i].Repository = to
		}
	}
	return release
}

// Keep only advisories referenced by readable releases. Remap their indexes
// after virtual member selection so denied or shadowed versions disclose none.
func compactAdvisories(reg hexfmt.RegistryPackage) hexfmt.RegistryPackage {
	reg.Releases = slices.Clone(reg.Releases)
	oldToNew := map[uint64]uint64{}
	var advisories [][]byte
	for i := range reg.Releases {
		indexes := slices.Clone(reg.Releases[i].AdvisoryIndexes)
		for j, old := range indexes {
			mapped, ok := oldToNew[old]
			if !ok {
				mapped = uint64(len(advisories))
				oldToNew[old] = mapped
				advisories = append(advisories, reg.Advisories[old])
			}
			indexes[j] = mapped
		}
		reg.Releases[i].AdvisoryIndexes = indexes
	}
	reg.Advisories = advisories
	return reg
}

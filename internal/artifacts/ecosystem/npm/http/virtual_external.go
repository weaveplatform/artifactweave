package npmhttp

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // npm wire compatibility
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const externalArchiveLimit = 128 << 20

func (h handler) externalError(w http.ResponseWriter, r *http.Request, err error) {
	p, _ := kernel.FromContext(r.Context())
	h.k.Error(w, r, pkgroute.SourceError(err, p.Anonymous || p.Name == ""))
}

func (h handler) externalGuard() pkgroute.ExternalGuard {
	return pkgroute.ExternalGuard{Kit: h.k, Sources: h.sources, Name: "npm"}
}

// Invalid cache configuration disables rewriting. Operational store errors keep
// their failure identity instead of silently advertising a direct internet URL.
func (h handler) externalRequest(ctx context.Context, r *http.Request) (*http.Request, *http.Request, error) {
	if !pkgroute.ExternalEnabled(pkgroute.RequestFrom(r).Repo) || h.sources == nil {
		return r, nil, nil
	}
	member, err := h.externalGuard().CacheRead(ctx, r)
	if errors.Is(err, source.ErrDenied) || errors.Is(err, kernel.ErrNotFound) {
		return r, nil, nil
	}
	if err != nil {
		return r, nil, fmt.Errorf("npm external cache member: %w", err)
	}
	pinned, err := h.externalGuard().Pin(r)
	if err != nil {
		return r, nil, fmt.Errorf("npm external virtual authority: %w", err)
	}
	return pinned, member, nil
}

func externalInvalid() error {
	return apierror.New(http.StatusBadGateway, "invalid external npm package")
}

func (h handler) recordExternal(r *http.Request, name, spec string) (string, error) {
	ext, ok := npmfmt.ExternalDependency(name, spec, h.format.ExternalDependenciesVersioning)
	if !ok {
		return spec, nil
	}
	if _, err := pkgroute.ExternalURL(pkgroute.RequestFrom(r).Repo, ext.URL); err != nil {
		return spec, nil
	}
	if err := h.externalGuard().Check(r.Context(), r); err != nil {
		return "", fmt.Errorf("npm external registration authority: %w", err)
	}
	rows, err := h.k.DB().NPMExternals().List(r.Context(), pkgroute.RequestFrom(r).Repo.Key, name)
	if err != nil {
		return "", fmt.Errorf("npm external dependency mappings: %w", err)
	}
	for _, row := range rows {
		if row.URL == ext.URL && row.File == ext.File {
			return row.Spec, nil
		}
	}
	binding := model.NPMExternal{Virtual: pkgroute.RequestFrom(r).Repo.Key, Name: name, Spec: ext.Spec, URL: ext.URL, File: ext.File}
	err = h.k.DB().NPMExternals().Put(r.Context(), binding)
	if errors.Is(err, kernel.ErrConflict) {
		// A dist-tag must not ambiguously name two URLs. Keep established tags stable
		// and give only a colliding new source a deterministic identity [I], #216.
		sum := sha256.Sum256([]byte(ext.URL))
		binding.Spec = strings.TrimSuffix(ext.Spec, "-art-external") + "-" + hex.EncodeToString(sum[:]) + "-art-external"
		err = h.k.DB().NPMExternals().Put(r.Context(), binding)
	}
	if err != nil {
		return "", fmt.Errorf("record npm external dependency: %w", err)
	}
	return binding.Spec, nil
}

func (h handler) rewriteExternal(r *http.Request, p *npmfmt.VirtualPackage) error {
	for _, number := range slices.Sorted(maps.Keys(p.Versions)) {
		v := p.Versions[number]
		manifest, err := npmfmt.RewriteDependencies(v.Manifest, func(name, spec string) (string, error) { return h.recordExternal(r, name, spec) })
		if err != nil {
			return fmt.Errorf("npm external manifest rewrite: %w", err)
		}
		v.Manifest = manifest
		p.Versions[number] = v
	}
	return nil
}

// External packages overlay the cached member model per request. Existing
// member versions win a clash [I]; neither registration nor fill invalidates R8.
func (h handler) externalPackage(r, member *http.Request, name string, p npmfmt.VirtualPackage) (npmfmt.VirtualPackage, func(context.Context) error, error) {
	if err := h.k.CheckDocument(r, documentPath(name)); err != nil {
		return p, nil, fmt.Errorf("npm external document policy: %w", err)
	}
	rows, err := h.k.DB().NPMExternals().List(r.Context(), pkgroute.RequestFrom(r).Repo.Key, name)
	if err != nil {
		return p, nil, fmt.Errorf("npm external package mappings: %w", err)
	}
	if p.Versions == nil {
		p = npmfmt.VirtualPackage{
			Name:     name,
			Versions: map[string]npmfmt.VirtualVersion{},
			Tags:     map[string]string{},
			Fields:   map[string]json.RawMessage{},
			Times:    map[string]json.RawMessage{},
		}
	}
	memberLatest := p.Tags[npmfmt.TagLatest]
	var checks []func(context.Context) error
	for _, row := range rows {
		if _, err := pkgroute.ExternalURL(pkgroute.RequestFrom(r).Repo, row.URL); err != nil {
			continue
		}
		read, t, err := h.readExternal(r, member, row, false)
		if err != nil {
			return p, nil, err
		}
		checks = append(checks, read.Revalidate)
		existing, present := p.Versions[t.Version]
		if present && existing.ExternalTarball != "" {
			var old struct {
				Dist struct {
					Shasum string `json:"shasum"`
				} `json:"dist"`
			}
			_ = json.Unmarshal(existing.Manifest, &old)
			digest := sha1.Sum(read.Data) //nolint:gosec // npm shasum compatibility
			if old.Dist.Shasum != hex.EncodeToString(digest[:]) {
				return p, nil, externalInvalid()
			}
		}
		if !present {
			var manifest remoteDocument
			_ = json.Unmarshal(t.Manifest, &manifest)
			sha := sha1.Sum(read.Data) //nolint:gosec // npm shasum/ETag compatibility
			strong := sha512.Sum512(read.Data)
			manifest["dist"] = encode(map[string]string{"shasum": hex.EncodeToString(sha[:]), "integrity": "sha512-" + base64.StdEncoding.EncodeToString(strong[:])})
			p.Versions[t.Version] = npmfmt.VirtualVersion{
				Manifest:         encode(manifest),
				HasInstallScript: t.HasInstallScript,
				Member:           pkgroute.RequestFrom(member).Repo.Key,
				ExternalTarball:  externalTarballPath(row),
			}
		}
		if _, exists := p.Tags[row.Spec]; !exists {
			p.Tags[row.Spec] = t.Version
		}
	}
	if len(p.Versions) == 0 {
		return p, nil, virtualNotFound(name)
	}
	if memberLatest == "" {
		var candidates []npmfmt.Candidate
		for _, number := range slices.Sorted(maps.Keys(p.Versions)) {
			candidates = append(candidates, npmfmt.Candidate{Version: number, Published: p.Versions[number].Published})
		}
		p.Tags[npmfmt.TagLatest] = h.format.Latest(candidates)
	}

	if err := h.rewriteExternal(r, &p); err != nil {
		return p, nil, err
	}
	check := func(ctx context.Context) error {
		for _, verify := range checks {
			if verify != nil {
				if err := verify(ctx); err != nil {
					return err
				}
			}
		}
		return h.externalGuard().Check(ctx, r)
	}
	return p, check, nil
}

func externalTarballPath(row model.NPMExternal) string {
	q := url.Values{"package": {row.Name}, "spec": {row.Spec}}
	return "/" + escapePath("_external/"+row.File) + "?" + q.Encode()
}

func (h handler) readExternal(r, member *http.Request, row model.NPMExternal, download bool) (pkgroute.ExternalRead, *npmfmt.Tarball, error) {
	guard := h.externalGuard()
	target, err := pkgroute.ExternalURL(pkgroute.RequestFrom(r).Repo, row.URL)
	if err != nil {
		return pkgroute.ExternalRead{}, nil, fmt.Errorf("npm external URL authority: %w", err)
	}
	path := "_external/" + row.File
	if !h.k.Can(member, path, api.ActionRead) {
		return pkgroute.ExternalRead{}, nil, source.ErrDenied
	}
	if err := guard.Check(r.Context(), r); err != nil {
		return pkgroute.ExternalRead{}, nil, fmt.Errorf("npm external virtual authority: %w", err)
	}
	cachedOnly := pkgroute.RequestFrom(member).CacheOnly || pkgroute.RequestFrom(member).Repo.Offline != nil && *pkgroute.RequestFrom(member).Repo.Offline
	if cachedOnly && row.SHA256 == "" {
		return pkgroute.ExternalRead{}, nil, kernel.ErrNotFound
	}
	validate := func(data []byte) error {
		t, err := npmfmt.ReadTarball(bytes.NewReader(data), int64(len(data)))
		if err != nil || t.Name != row.Name {
			return externalInvalid()
		}
		return nil
	}
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              path,
		Method:            http.MethodGet,
		ExternalURL:       target.String(),
		NoCrossOriginAuth: true,
		Variant:           "npm-external-v1",
		CheckURL:          pkgroute.ExternalRedirect(pkgroute.RequestFrom(r).Repo, target, func(ctx context.Context) error { return guard.Check(ctx, r) }),
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if size <= 0 || size > externalArchiveLimit {
				return externalInvalid()
			}
			data, err := io.ReadAll(io.LimitReader(body, externalArchiveLimit+1))
			if err != nil {
				return fmt.Errorf("npm external admission copy: %w", item.MarkStorageFault(ctx, err, externalInvalid()))
			}
			if int64(len(data)) != size {
				return externalInvalid()
			}
			if err := validate(data); err != nil {
				return err
			}
			return guard.Check(ctx, r)
		},
	}
	// Cache-only artifact reads do not have a URL-aware remote entry. Pin their
	// bytes to this persisted source binding, while retaining all source checks.
	if cachedOnly {
		resource.Checksums.SHA256 = row.SHA256
	}
	read, err := h.k.ReadExternal(r, member, resource, download, externalArchiveLimit, externalInvalid(), validate, func(ctx context.Context) error { return guard.Check(ctx, r) })
	if err != nil {
		return read, nil, fmt.Errorf("npm external dependency: %w", err)
	}
	t, err := npmfmt.ReadTarball(bytes.NewReader(read.Data), int64(len(read.Data)))
	if err != nil {
		return read, nil, externalInvalid()
	}
	digest := sha256.Sum256(read.Data)
	row.SHA256 = hex.EncodeToString(digest[:])
	if err = h.k.DB().NPMExternals().Put(r.Context(), row); err != nil {
		return read, nil, fmt.Errorf("npm external byte identity: %w", err)
	}
	return read, t, nil
}

func (h handler) externalTarball(ctx context.Context, w http.ResponseWriter, r *http.Request, path string) {
	r, member, err := h.externalRequest(ctx, r)
	if err != nil {
		h.externalError(w, r, err)
		return
	}
	if member == nil {
		h.k.Error(w, r, notFound(r))
		return
	}
	q := r.URL.Query()
	if len(q) != 2 || len(q["package"]) != 1 || len(q["spec"]) != 1 {
		h.k.Error(w, r, notFound(r))
		return
	}
	if !h.k.Require(w, member, path, api.ActionRead) {
		return
	}
	row, err := h.k.DB().NPMExternals().Get(ctx, pkgroute.RequestFrom(r).Repo.Key, q.Get("package"), q.Get("spec"))
	if err != nil {
		h.externalError(w, r, err)
		return
	}
	if path != "_external/"+row.File {
		h.k.Error(w, r, notFound(r))
		return
	}
	if err = h.k.CheckDownload(r, path); err != nil {
		h.externalError(w, r, err)
		return
	}
	read, _, err := h.readExternal(r, member, row, true)
	if err != nil {
		h.externalError(w, r, err)
		return
	}
	sha := sha1.Sum(read.Data) //nolint:gosec // npm shasum/ETag compatibility
	result := source.Result{
		Used:       read.Used,
		Revalidate: read.Revalidate,
		Stream: &source.Stream{
			Body:           &remoteJSONBody{Reader: bytes.NewReader(read.Data)},
			Status:         http.StatusOK,
			Representation: source.Representation{Path: path, Size: int64(len(read.Data)), MediaType: "application/octet-stream", ETag: hex.EncodeToString(sha[:])},
		},
	}
	h.k.ServeResult(w, r, result)
}

package luarockshttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	luafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The virtual class is served by the protocol's package-aware resolver. A
// generic Item lookup cannot merge manifests or read transient remote rocks.
type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func responseRepoKey(r *http.Request) string {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/artifactory/api/luarocks/"); ok {
		key, _, _ := strings.Cut(rest, "/")
		return key
	}
	return pkgroute.RequestFrom(r).Repo.Key
}

// virtualDeployment returns the member request an upload through the
// virtual repository of r writes with: its default deployment repository,
// a local or federated member (virtual.DeployTarget), on whose root the
// caller needs action. JFrog's LuaRocks page uploads to "virtual
// repositories that include a local repository with a deployment target",
// and 7.x asks Deploy of that repository alone, not of the virtual
// repository (RTFACT-17787 for RubyGems). Without one 7.x answers 405 with
// Allow: GET before it checks any permission (decision 0008, R6;
// UploadServiceUtils.getTargetRepository 248-261, 7.161.26). One of a type
// the virtual repository does not take, which only data saved before the
// save checks can name (R11), is refused as a member request is.
func (h handler) virtualDeployment(
	w http.ResponseWriter, r *http.Request, action api.Actions,
) (*http.Request, bool) {
	v := pkgroute.RequestFrom(r).Repo
	target, err := virtual.DeployTarget(r.Context(), h.k.DB().Repos(), v)
	switch {
	case errors.Is(err, virtual.ErrNoDeployTarget):
		h.k.Error(w, r, itemkit.NoDeployTarget(v.Key))
		return nil, false
	case err != nil:
		h.k.Error(w, r, fmt.Errorf("luarocks: %w", err))
		return nil, false
	}
	mr, err := h.k.MemberRequest(r, target)
	if err != nil {
		h.k.Error(w, r, err)
		return nil, false
	}
	if !h.k.CanFolder(mr, "", action) {
		h.k.Error(w, r, apierror.New(http.StatusForbidden, "deployment target is not authorized"))
		return nil, false
	}
	return mr, true
}

// virtualRead serves a manifest or a rock through the virtual repository.
// A rock comes from the first member, in the package handlers' walk
// (MergeMembers: decision 0008's R1 order, nested virtual repositories
// flattened, a cycle skipped), that holds it and lets the caller read it.
// The walk passes over a member of another type, one that is blacked out or
// whose patterns reject the rock, and one the caller may read nothing in;
// a member without the rock, one whose Read refuses it and one whose
// upstream cannot be reached are passed over too (passedOver), so the
// answer is the virtual repository's own 404 (R3). The virtual repository's
// own patterns leave it no member to search, which 6.20's
// VirtualRepoDownloadStrategy answers "No repository found to serve the
// request" (decision 0032, R5).
func (h handler) virtualRead(w http.ResponseWriter, r *http.Request, path string) {
	if r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid LuaRocks query"))
		return
	}
	if manifestPath(path) {
		h.virtualManifest(w, r, path)
		return
	}
	if _, ok := luafmt.Parse(path); !ok {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
		return
	}
	members, err := h.k.MergeMembers(r, path)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteRock(mr, path)
			if pkgroute.PassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			if err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			h.k.ServeResult(w, mr, result)
			return
		}
		found, err := h.k.ResolveDownloadItem(mr, path)
		if pkgroute.PassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if found.Item.Folder {
			continue
		}
		h.k.ServeFound(w, mr, found)
		return
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
}

// manifestPart is one member's share of a merged manifest: the files its
// manifest lists that the caller may read, and the checks its source asks
// again before the merged manifest is delivered.
type manifestPart struct {
	entries []luafmt.Entry[luaValue]
	check   func(context.Context) error
}

// virtualManifest merges the manifest at path of each member in the
// package handlers' walk (pkgroute.Collect) with luafmt.Merger, in R1's
// order. The caller needs Read on the root of some member (RequireRoot),
// and each member's rows take that member's Read alone. The virtual
// repository's own patterns rejecting the manifest refuse it, and a member
// that is blacked out or whose patterns reject the manifest's path is left
// out, remote members included: 7.x ignores a blacked-out repository when
// it resolves (R5). A member without the manifest is passed over, and so is
// one whose upstream fails or cannot be reached, with a warning
// (WarnPassedOver) [I].
func (h handler) virtualManifest(w http.ResponseWriter, r *http.Request, path string) {
	if !h.k.RequireRoot(w, r) {
		return
	}
	parts, err := pkgroute.Collect(h.k, r, path, func(mr *http.Request) (manifestPart, error) {
		part, err := h.memberPart(mr, path)
		h.k.WarnPassedOver(mr, err)
		return part, err
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	entries := make([]virtual.Part[[]luafmt.Entry[luaValue]], len(parts))
	var revalidate []func(context.Context) error
	for i, p := range parts {
		entries[i] = virtual.Part[[]luafmt.Entry[luaValue]]{Member: p.Member, Doc: p.Doc.entries}
		if p.Doc.check != nil {
			revalidate = append(revalidate, p.Doc.check)
		}
	}
	merged, err := luafmt.Merger[luaValue]{}.Merge(r.Context(), entries)
	if err == nil {
		var data []byte
		if data, err = renderManifest(merged); err == nil {
			h.serveManifest(w, r, data, revalidate)
			return
		}
	}
	h.k.Error(w, r, err)
}

// memberManifest reads the manifest at path of the member r names, with
// only the rows the caller may read in that member, and, for a remote, the
// checks its source asks again before the merged manifest is delivered.
func (h handler) memberManifest(
	r *http.Request, path string,
) (remoteManifest, func(context.Context) error, error) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		metadata, manifest, err := h.upstreamManifest(r, path, false)
		if err != nil {
			return remoteManifest{}, nil, err
		}
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(r.Context()); err != nil {
				return remoteManifest{}, nil, fmt.Errorf("luarocks manifest authority: %w", err)
			}
		}
		// A rock whose file the member's patterns reject stays in the merge
		// (R3, R5), so the rows take Read permission alone.
		filtered, paths, err := filteredMemberManifest(
			r.Context(),
			r,
			h.k,
			manifest,
			metadata.CheckPermission,
		)
		if err != nil {
			return remoteManifest{}, nil, err
		}
		return filtered, func(ctx context.Context) error {
			if metadata.Revalidate != nil {
				if err := metadata.Revalidate(ctx); err != nil {
					return fmt.Errorf("luarocks virtual manifest authority: %w", err)
				}
			}
			if metadata.CheckPermission != nil {
				for _, path := range paths {
					if err := metadata.CheckPermission(ctx, path); err != nil {
						return fmt.Errorf("luarocks virtual package authority: %w", err)
					}
				}
			}
			return nil
		}, nil
	}
	f, err := h.k.ResolveStoredItem(r, path)
	if err != nil {
		return remoteManifest{}, nil, fmt.Errorf("luarocks member manifest: %w", err)
	}
	if f.Item.Folder {
		return remoteManifest{}, nil, kernel.ErrNotFound
	}
	rc, _, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		return remoteManifest{}, nil, fmt.Errorf("luarocks member manifest blob: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, remoteManifestLimit+1))
	if err != nil {
		return remoteManifest{}, nil, fmt.Errorf("luarocks member manifest body: %w", err)
	}
	manifest, err := parseRemoteManifest(data)
	if err != nil {
		return remoteManifest{}, nil, err
	}
	filtered, _, err := filteredMemberManifest(r.Context(), r, h.k, manifest, nil)
	return filtered, nil, err
}

// filteredMemberManifest is manifest without the rows of files the caller
// may not read in the member r names, or that the member's source check
// refuses; a check that fails otherwise fails the manifest.
func filteredMemberManifest(
	ctx context.Context, r *http.Request, k *pkgroute.Kit, manifest remoteManifest,
	check func(context.Context, string) error,
) (remoteManifest, []string, error) {
	var checkErr error
	data, paths, err := manifest.filtered(func(path string) bool {
		if !k.Can(r, path, api.ActionRead) {
			return false
		}
		if check != nil {
			if err := check(ctx, path); err != nil {
				if !errors.Is(err, source.ErrDenied) {
					checkErr = err
				}
				return false
			}
		}
		return true
	})
	if err != nil || checkErr != nil {
		return remoteManifest{}, nil, remoteFailure()
	}
	filtered, err := parseRemoteManifest(data)
	return filtered, paths, err
}

// memberPart reads one member's manifest at path for a merge.
func (h handler) memberPart(mr *http.Request, path string) (manifestPart, error) {
	manifest, check, err := h.memberManifest(mr, path)
	if err != nil {
		return manifestPart{}, err
	}
	entries, err := manifest.entries()
	if err != nil {
		return manifestPart{}, err
	}
	return manifestPart{entries: entries, check: check}, nil
}

// serveManifest answers the merged manifest data, asking each member's
// source checks again before it is delivered.
func (h handler) serveManifest(
	w http.ResponseWriter, r *http.Request, data []byte, revalidate []func(context.Context) error,
) {
	digest := sha256.Sum256(data)
	h.k.ServeResult(w, r, source.Result{Revalidate: func(ctx context.Context) error {
		for _, check := range revalidate {
			if err := check(ctx); err != nil {
				return err
			}
		}
		return nil
	}, Stream: &source.Stream{
		Status: http.StatusOK,
		Body:   io.NopCloser(bytes.NewReader(data)),
		Representation: source.Representation{
			MediaType: "text/plain; charset=utf-8",
			Size:      int64(len(data)),
			ETag:      fmt.Sprintf(`"%x"`, digest),
			Checksums: kernel.Checksums{
				SHA256: fmt.Sprintf("%x", digest),
			},
		},
	}})
}

// mergeManifests merges manifests, in member order, as virtualManifest
// does: luafmt.Merger over the files each lists, rendered back as one
// manifest.
func mergeManifests(manifests []remoteManifest) ([]byte, error) {
	parts := make([]virtual.Part[[]luafmt.Entry[luaValue]], 0, len(manifests))
	for _, m := range manifests {
		entries, err := m.entries()
		if err != nil {
			return nil, err
		}
		parts = append(parts, virtual.Part[[]luafmt.Entry[luaValue]]{Doc: entries})
	}
	merged, err := luafmt.Merger[luaValue]{}.Merge(context.Background(), parts)
	if err != nil {
		return nil, err //nolint:wrapcheck // a merge never fails
	}
	return renderManifest(merged)
}

// entries is the files m lists, in its order, each with its record: the
// input luafmt.Merger takes. A record without a safe architecture and file
// name fails the manifest.
func (m remoteManifest) entries() ([]luafmt.Entry[luaValue], error) {
	var out []luafmt.Entry[luaValue]
	for _, pkg := range m.repository.fields {
		for _, version := range pkg.value.fields {
			for _, record := range version.value.fields {
				arch, err := recordArch(record.value)
				if err != nil {
					return nil, err
				}
				if _, err := manifestFilename(pkg.key, version.key, arch); err != nil {
					return nil, err
				}
				out = append(out, luafmt.Entry[luaValue]{
					Name: pkg.key, Version: version.key, Arch: arch, Record: record.value,
				})
			}
		}
	}
	return out, nil
}

// renderManifest writes entries back as one manifest: each package where
// its first file is listed, each of its versions likewise, and each record
// in order, so LuaRocks sees one coherent repository table.
func renderManifest(entries []luafmt.Entry[luaValue]) ([]byte, error) {
	merged := luaValue{kind: '{'}
	packages := map[string]int{}
	versions := map[string]map[string]int{}
	for _, e := range entries {
		pi, ok := packages[e.Name]
		if !ok {
			pi = len(merged.fields)
			packages[e.Name] = pi
			versions[e.Name] = map[string]int{}
			merged.fields = append(merged.fields, luaField{key: e.Name, value: luaValue{kind: '{'}})
		}
		vi, ok := versions[e.Name][e.Version]
		if !ok {
			vi = len(merged.fields[pi].value.fields)
			versions[e.Name][e.Version] = vi
			merged.fields[pi].value.fields = append(merged.fields[pi].value.fields,
				luaField{key: e.Version, value: luaValue{kind: '{'}})
		}
		records := &merged.fields[pi].value.fields[vi].value.fields
		*records = append(*records, luaField{value: e.Record})
	}
	var b bytes.Buffer
	b.WriteString("commands = {}\nmodules = {}\nrepository = ")
	if err := writeLuaValue(&b, merged, 0); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	if b.Len() > remoteManifestLimit {
		return nil, errRemoteManifest
	}
	return b.Bytes(), nil
}

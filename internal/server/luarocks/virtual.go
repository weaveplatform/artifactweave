package luarocks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	luafmt "github.com/weaveplatform/artifactweave/internal/format/luarocks"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The virtual class is served by the protocol's package-aware resolver. A
// generic Item lookup cannot merge manifests or read transient remote rocks.
type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	store.Store,
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

// virtualMembers validates the configured members at use time: a repository
// can change class or type after the virtual was created. The repository
// list's order is the resolution order, including when members share a rock.
func (h handler) virtualMembers(r *http.Request) ([]*http.Request, error) {
	virtual := pkgroute.RequestFrom(r).Repo
	if virtual.Class != repo.Virtual {
		return nil, apierror.New(http.StatusBadRequest, "not a virtual repository")
	}
	seen := map[string]bool{virtual.Key: true}
	var out []*http.Request
	for _, key := range virtual.Repositories {
		if seen[key] {
			return nil, apierror.New(http.StatusBadRequest, "invalid virtual repository member")
		}
		seen[key] = true
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("luarocks virtual member %s: %w", key, err)
		}
		if member.Class != repo.Local && member.Class != repo.Remote {
			return nil, apierror.New(http.StatusBadRequest,
				"unsupported virtual LuaRocks member class")
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("luarocks virtual member %s: %w", key, err)
		}
		out = append(out, mr)
	}
	return out, nil
}

func (h handler) virtualDeployment(
	w http.ResponseWriter, r *http.Request, action types.Actions,
) (*http.Request, bool) {
	if !h.k.RequireFolder(w, r, "", action) {
		return nil, false
	}
	virtual := pkgroute.RequestFrom(r).Repo
	if virtual.DefaultDeploymentRepo == "" ||
		!slices.Contains(virtual.Repositories, virtual.DefaultDeploymentRepo) {
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed,
			"virtual LuaRocks repository has no local deployment target"))
		return nil, false
	}
	target, err := h.k.DB().Repos().Get(r.Context(), virtual.DefaultDeploymentRepo)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.k.Error(w, r, apierror.New(http.StatusBadRequest,
				"virtual LuaRocks deployment target is missing"))
			return nil, false
		}
		h.k.Error(w, r, err)
		return nil, false
	}
	if target.Class != repo.Local {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest,
			"virtual LuaRocks deployment target must be local"))
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
	if !h.k.Require(w, r, path, types.ActionRead) {
		return
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if !h.k.Can(mr, path, types.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteRock(mr, path)
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
		found, err := h.k.ResolveItem(mr, path)
		if errors.Is(err, store.ErrNotFound) {
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

func (h handler) memberManifest(
	r *http.Request, path string,
) (remoteManifest, func(context.Context) error, error) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		metadata, manifest, err := h.upstreamManifest(r, path)
		if err != nil {
			return remoteManifest{}, nil, err
		}
		if metadata.Revalidate != nil {
			if err := metadata.Revalidate(r.Context()); err != nil {
				return remoteManifest{}, nil, fmt.Errorf("luarocks manifest authority: %w", err)
			}
		}
		filtered, paths, err := filteredMemberManifest(
			r.Context(),
			r,
			h.k,
			manifest,
			metadata.CheckRead,
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
			if metadata.CheckRead != nil {
				for _, path := range paths {
					if err := metadata.CheckRead(ctx, path); err != nil {
						return fmt.Errorf("luarocks virtual package authority: %w", err)
					}
				}
			}
			return nil
		}, nil
	}
	f, err := h.k.ResolveItem(r, path)
	if err != nil {
		return remoteManifest{}, nil, fmt.Errorf("luarocks member manifest: %w", err)
	}
	if f.Item.Folder {
		return remoteManifest{}, nil, store.ErrNotFound
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

func filteredMemberManifest(
	ctx context.Context, r *http.Request, k *pkgroute.Kit, manifest remoteManifest,
	check func(context.Context, string) error,
) (remoteManifest, []string, error) {
	var checkErr error
	data, paths, err := manifest.filtered(func(path string) bool {
		if !k.Can(r, path, types.ActionRead) {
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

func (h handler) virtualManifest(w http.ResponseWriter, r *http.Request, path string) {
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var manifests []remoteManifest
	var revalidate []func(context.Context) error
	for _, mr := range members {
		if !h.k.CanFolder(mr, "", types.ActionRead) {
			continue
		}
		manifest, check, err := h.memberManifest(mr, path)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		manifests = append(manifests, manifest)
		if check != nil {
			revalidate = append(revalidate, check)
		}
	}
	data, err := mergeManifests(manifests)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
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
			Checksums: types.Checksums{
				SHA256: fmt.Sprintf("%x", digest),
			},
		},
	}})
}

// mergeManifests keeps the first member's record for one exact rock identity
// while retaining other versions, revisions and architectures from later
// members. LuaRocks sees one coherent repository table, not member URLs.
func mergeManifests(manifests []remoteManifest) ([]byte, error) {
	merged := luaValue{kind: '{'}
	packages := map[string]int{}
	versions := map[string]map[string]int{}
	files := map[string]bool{}
	for _, manifest := range manifests {
		for _, pkg := range manifest.repository.fields {
			pi, ok := packages[pkg.key]
			if !ok {
				pi = len(merged.fields)
				packages[pkg.key] = pi
				versions[pkg.key] = map[string]int{}
				merged.fields = append(
					merged.fields,
					luaField{key: pkg.key, value: luaValue{kind: '{'}},
				)
			}
			for _, version := range pkg.value.fields {
				vi, ok := versions[pkg.key][version.key]
				if !ok {
					vi = len(merged.fields[pi].value.fields)
					versions[pkg.key][version.key] = vi
					merged.fields[pi].value.fields = append(merged.fields[pi].value.fields,
						luaField{key: version.key, value: luaValue{kind: '{'}})
				}
				for _, record := range version.value.fields {
					arch, err := recordArch(record.value)
					if err != nil {
						return nil, err
					}
					filename, err := manifestFilename(pkg.key, version.key, arch)
					if err != nil {
						return nil, err
					}
					if files[filename] {
						continue
					}
					files[filename] = true
					records := &merged.fields[pi].value.fields[vi].value.fields
					*records = append(*records, record)
				}
			}
		}
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

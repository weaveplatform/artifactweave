package pypihttp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Virtual reads need package-aware models, never a stored virtual item.
type virtualSource struct{}

func (virtualSource) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (h handler) virtualWrite(w http.ResponseWriter, r *http.Request) bool {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Virtual {
		return false
	}
	h.k.Error(w, r, apierror.New(http.StatusNotImplemented, fmt.Sprintf("'%s' is a virtual repository, which artifactweave does not serve through the pypi API yet", c.Key)))
	return true
}

type virtualFile struct {
	remoteFile
	file   pypifmt.File
	check  func(context.Context) error
	member *http.Request
}

type virtualProject = pypifmt.Project[virtualFile]

func localVirtualFile(f pypifmt.File, version string) virtualFile {
	attrs := map[string]string{}
	if f.RequiresPython != "" {
		attrs["data-requires-python"] = f.RequiresPython
	}
	if f.Yanked {
		attrs["data-yanked"] = f.YankedReason
	}
	rf := remoteFile{
		name:    f.Name,
		path:    f.Path,
		version: version,
		hashes:  map[string]string{"sha256": f.SHA256},
		attrs:   attrs,
		doc:     remoteDoc{"size": encode(f.Size), "upload-time": encode(f.Uploaded.UTC().Format(uploadTime))},
	}
	if f.MD5 != "" {
		rf.hashes["md5"] = f.MD5
	}
	if f.Core {
		rf.hasCoreMetadata = true
		rf.metadata = map[string]string{"sha256": f.Metadata}
	}
	return virtualFile{remoteFile: rf, file: f}
}

func remoteVirtualFile(r *http.Request, f remoteFile, result source.Result) virtualFile {
	file := pypifmt.File{Path: f.path, Name: f.name, SHA256: f.hashes["sha256"], MD5: f.hashes["md5"], RequiresPython: f.attrs["data-requires-python"]}
	file.YankedReason, file.Yanked = f.attrs["data-yanked"]
	if raw := f.doc["size"]; raw != nil {
		_ = json.Unmarshal(raw, &file.Size)
	}
	var stamp string
	if raw := f.doc["upload-time"]; raw != nil && json.Unmarshal(raw, &stamp) == nil {
		file.Uploaded, _ = time.Parse(time.RFC3339Nano, stamp)
	}
	fenceRemote(&result, []string{f.path})
	return virtualFile{remoteFile: f, file: file, check: result.Revalidate, member: r}
}

func (h handler) memberProject(r *http.Request, name, lookup string) (virtualProject, error) {
	c := pkgroute.RequestFrom(r).Repo
	out := virtualProject{Name: name}
	if c.Class == repo.Remote {
		index, result, err := h.upstreamIndex(r, name, lookup)
		if err != nil {
			return out, err
		}
		versions := map[string]int{}
		for _, f := range index.files {
			if !h.k.Can(r, f.path, api.ActionRead) {
				continue
			}
			if result.CheckPermission != nil {
				if err = result.CheckPermission(r.Context(), f.path); err != nil {
					if errors.Is(err, source.ErrDenied) {
						continue
					}
					return out, fmt.Errorf("pypi member authority: %w", err)
				}
			}
			i, ok := versions[f.version]
			if !ok {
				i = len(out.Releases)
				versions[f.version] = i
				out.Releases = append(out.Releases, pypifmt.Release[virtualFile]{Version: f.version})
			}
			out.Releases[i].Files = append(out.Releases[i].Files, pypifmt.Distribution[virtualFile]{Name: f.name, Path: f.path, Value: remoteVirtualFile(r, f, result)})
		}
	} else {
		p, ok, err := load(r.Context(), h.k.DB(), c.Key, name, h.reads(r))
		if err != nil {
			return out, err
		}
		if !ok {
			return out, kernel.ErrNotFound
		}
		for _, rel := range p.releases {
			first := slices.MinFunc(rel.files, func(a, b pypifmt.File) int {
				return cmp.Or(a.Uploaded.Compare(b.Uploaded), cmp.Compare(a.Path, b.Path))
			})
			release := pypifmt.Release[virtualFile]{Version: rel.version, Info: rel.meta.Text(first)}
			for _, f := range rel.files {
				release.Files = append(
					release.Files,
					pypifmt.Distribution[virtualFile]{Name: f.Name, Path: f.Path, Value: localVirtualFile(f, rel.version), Info: rel.meta.Text(f), Uploaded: f.Uploaded},
				)
			}
			out.Releases = append(out.Releases, release)
		}
	}
	if len(out.Releases) == 0 {
		return out, kernel.ErrNotFound
	}
	return out, nil
}

func (h handler) mergedProject(r *http.Request, name string) (virtualProject, error) {
	members, err := h.k.MergeMembers(r, pagePath(name))
	if err != nil {
		return virtualProject{}, fmt.Errorf("pypi virtual members: %w", err)
	}
	parts := make([]virtual.Part[virtualProject], 0, len(members))
	priority := false
	for _, mr := range members {
		member := pkgroute.RequestFrom(mr).Repo
		if priority && !member.PriorityResolution.Bool() {
			break
		}
		project, err := h.memberProject(mr, name, "")
		if pypiPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			return virtualProject{}, err
		}
		parts = append(parts, virtual.Part[virtualProject]{Member: member, Doc: project})
		if member.PriorityResolution.Bool() {
			priority = true
		}
	}
	c := pkgroute.RequestFrom(r).Repo
	merged, err := (pypifmt.Merger[virtualFile]{Allowed: func(path string) bool { return generic.CheckDownload(c, path, false) == nil }}).Merge(
		r.Context(),
		parts,
	) //nolint:wrapcheck // the pure merger owns its error
	if err != nil {
		return virtualProject{}, fmt.Errorf("pypi virtual merge: %w", err)
	}
	return merged, nil
}

func virtualFiles(p virtualProject) ([]remoteFile, []func(context.Context) error) {
	var files []remoteFile
	var checks []func(context.Context) error
	for _, rel := range p.Releases {
		for _, f := range rel.Files {
			rf := f.Value.remoteFile
			// Remote renderers populate their copies of the parsed JSON document.
			rf.doc = maps.Clone(rf.doc)
			files = append(files, rf)
			if f.Value.check != nil {
				checks = append(checks, f.Value.check)
			}
		}
	}
	return files, checks
}

func (h handler) revalidateVirtual(w http.ResponseWriter, r *http.Request, checks []func(context.Context) error) bool {
	for _, check := range checks {
		if err := check(r.Context()); err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return false
		}
	}
	return true
}

func (h handler) virtualProject(w http.ResponseWriter, r *http.Request, name string) {
	if !h.k.RequireRoot(w, r) {
		return
	}
	p, err := h.mergedProject(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(p.Releases) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, fmt.Sprintf("Project '%s' was not found", name)))
		return
	}
	files, checks := virtualFiles(p)
	if !h.revalidateVirtual(w, r, checks) {
		return
	}
	if h.json(r) {
		writePage(w, r, jsonType, true, remoteProjectJSON(remoteIndex{}, files, name, "../..", h.format.CoreMetadata))
		return
	}
	writePage(w, r, htmlType, true, renderRemoteHTML(files, "../..", h.format.CoreMetadata, true))
}

func (h handler) memberProjects(r *http.Request, v repo.Config) ([]string, []func(context.Context) error, error) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		names, err := projects(r.Context(), h.k.DB(), pkgroute.RequestFrom(r).Repo.Key, func(path string) bool {
			return h.k.Can(r, path, api.ActionRead) && generic.CheckDownload(v, path, false) == nil
		})
		return names, nil, err
	}
	index, result, err := h.upstreamIndex(r, "", "")
	if err != nil {
		return nil, nil, err
	}
	names := []string{}
	checks := []func(context.Context) error{}
	lookups := 0
	for _, name := range index.projects {
		if !h.k.CanFolder(r, name, api.ActionRead) {
			continue
		}
		lookups++
		if lookups > 256 {
			return nil, nil, remoteError()
		}
		child, err := h.memberProject(r, name, pagePath(name))
		if pypiPassedOver(err) {
			h.k.WarnPassedOver(r, err)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		allowed := false
		for _, rel := range child.Releases {
			for _, file := range rel.Files {
				if generic.CheckDownload(v, file.Path, false) == nil {
					allowed = true
				}
			}
		}
		if !allowed {
			continue
		}
		_, childChecks := virtualFiles(child)
		names = append(names, name)
		checks = append(checks, childChecks...)
	}
	if result.Revalidate != nil {
		checks = append(checks, result.Revalidate)
	}
	return names, checks, nil
}

func (h handler) virtualList(w http.ResponseWriter, r *http.Request) {
	if !h.k.RequireRoot(w, r) {
		return
	}
	type listPart struct {
		names  []string
		checks []func(context.Context) error
	}
	parts, err := pkgroute.Collect(h.k, r, pagePath(""), func(mr *http.Request) (listPart, error) {
		names, checks, err := h.memberProjects(mr, pkgroute.RequestFrom(r).Repo)
		h.k.WarnPassedOver(mr, err)
		if err != nil && pypiPassedOver(err) {
			err = fmt.Errorf("%w: %w", virtual.ErrPassOver, err)
		}
		return listPart{names: names, checks: checks}, err
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	union := make([]virtual.Part[[]string], 0, len(parts))
	var checks []func(context.Context) error
	for _, part := range parts {
		union = append(union, virtual.Part[[]string]{Member: part.Member, Doc: part.Doc.names})
		checks = append(checks, part.Doc.checks...)
	}
	names, err := (pypifmt.ListMerger{}).Merge(r.Context(), union)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.revalidateVirtual(w, r, checks) {
		return
	}
	if h.json(r) {
		writePage(w, r, jsonType, true, listJSON(names))
	} else {
		writePage(w, r, htmlType, true, listHTML(names))
	}
}

func (h handler) virtualWarehouse(w http.ResponseWriter, r *http.Request, name, version string) {
	if !pypifmt.ValidName(name) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Project or version was not found"))
		return
	}
	if !h.k.RequireRoot(w, r) {
		return
	}
	name = pypifmt.Normalize(name)
	merged, err := h.mergedProject(r, name)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	p := project{name: name}
	infos := map[string]string{}
	owners := map[string]virtualFile{}
	releases := map[string][]map[string]any{}
	for _, rel := range merged.Releases {
		releaseModel := release{version: rel.Version}
		infos[rel.Version] = rel.Info
		owners[rel.Version] = rel.Owner
		urls := make([]map[string]any, 0, len(rel.Files))
		for _, f := range rel.Files {
			releaseModel.files = append(releaseModel.files, f.Value.file)
			doc := distributionJSON(f.Value.file, repoBase(r))
			doc["digests"] = maps.Clone(f.Value.hashes)
			urls = append(urls, doc)
		}
		p.releases = append(p.releases, releaseModel)
		releases[rel.Version] = urls
	}
	selected, ok := jsonRelease(p, version)
	if !ok {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "Project or version was not found"))
		return
	}
	first := selected.files[0]
	info := jsonInfo(infos[selected.version], name, selected.version, first.RequiresPython)
	var ownerCheck func(context.Context) error
	owner := owners[selected.version]
	if owner.member != nil {
		warehouse, result, err := h.upstreamWarehouse(owner.member, name, selected.version)
		if err != nil && !warehouseMissing(err) {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		if err == nil {
			if json.Unmarshal(warehouse.doc["info"], &info) != nil {
				h.k.Error(w, r, remoteError())
				return
			}
			fenceRemote(&result, []string{owner.path})
			ownerCheck = result.Revalidate
		}
	}

	anyAvailable := slices.ContainsFunc(selected.files, func(f pypifmt.File) bool { return !f.Yanked })
	info["yanked"], info["yanked_reason"] = !anyAvailable, nil
	if !anyAvailable {
		info["yanked_reason"] = optional(first.YankedReason)
	}
	_, checks := virtualFiles(merged)
	if ownerCheck != nil {
		checks = append(checks, ownerCheck)
	}
	if !h.revalidateVirtual(w, r, checks) {
		return
	}
	doc := map[string]any{"info": info, "urls": releases[selected.version]}
	_ = WriteMetadata(w, doc, r.Method == http.MethodHead)
}

func (h handler) virtualDownload(w http.ResponseWriter, r *http.Request, p pkgroute.Path) {
	if len(p.Segments) == 0 || p.Folder {
		h.k.Error(w, r, errNoFile)
		return
	}
	path := p.String()
	owner, metadata := strings.CutSuffix(path, metadataSuffix)
	metadata = metadata && strings.HasSuffix(owner, ".whl")
	policyPath := path
	if metadata {
		policyPath = owner
	}
	members, err := h.k.DownloadMembers(r, policyPath)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			if !h.k.Can(mr, policyPath, api.ActionRead) {
				continue
			}
			result, err := h.remoteArtifact(mr, p)
			if pypiPassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			h.k.ServeResult(w, mr, result)
			return
		}
		served, err := h.virtualStoredFile(w, mr, path, owner, metadata)
		if pypiPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if served {
			return
		}
	}
	h.k.Error(w, r, errNoFile)
}

func (h handler) virtualStoredFile(w http.ResponseWriter, r *http.Request, path, owner string, metadata bool) (bool, error) {
	if metadata && h.format.CoreMetadata {
		text, found, err := h.coreMetadata(r, owner)
		if err != nil {
			return false, fmt.Errorf("pypi virtual file: %w", err)
		}
		if found {
			if !h.k.Can(r, owner, api.ActionRead) {
				return false, kernel.ErrNotFound
			}
			if err = h.k.CheckDownload(r, owner); err != nil {
				return false, fmt.Errorf("pypi virtual file: %w", err)
			}
			writePage(w, r, "application/octet-stream", false, []byte(text))
			return true, nil
		}
	}
	if !h.k.Can(r, path, api.ActionRead) {
		return false, kernel.ErrNotFound
	}
	var found pkgroute.Found
	var err error
	if metadata {
		found, err = h.k.StoredItem(r, path)
	} else {
		found, err = h.k.ResolveDownloadItem(r, path)
	}
	if err != nil {
		return false, fmt.Errorf("pypi virtual file: %w", err)
	}
	if found.Item.Folder {
		return false, kernel.ErrNotFound
	}
	if metadata {
		if err = h.k.CheckDownload(r, owner); err != nil {
			return false, fmt.Errorf("pypi virtual file: %w", err)
		}
		h.k.ServeMetadata(w, r, path)
	} else {
		h.k.ServeFound(w, r, found)
	}
	return true, nil
}

// An absent upstream Warehouse endpoint may use the simple model. Policy,
// authorization and cache-fill refusals remain refusals even if they wrap a miss.
func warehouseMissing(err error) bool {
	if errors.As(err, new(*source.CacheFillDenied)) || pkgroute.IsRefusal(err) {
		return false
	}
	if errors.Is(err, kernel.ErrNotFound) {
		return true
	}
	var httpError *source.HTTPError
	return errors.As(err, &httpError) && httpError.Status == http.StatusNotFound
}

// A remote's credential refusal is a member miss during discovery, never a
// challenge for this virtual's caller. Selected-owner Warehouse info is separate.
func pypiPassedOver(err error) bool {
	if pkgroute.PassedOver(err) {
		return true
	}
	var upstream *source.HTTPError
	if errors.As(err, &upstream) {
		return upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden
	}
	var answer *apierror.Error
	return errors.As(err, &answer) && answer.NoChallenge && (answer.Status == http.StatusUnauthorized || answer.Status == http.StatusForbidden)
}

package terraformhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
)

func (h handler) virtualProvider(w http.ResponseWriter, r *http.Request, q providerOp) {
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.virtualModuleError(w, r, err)
		return
	}
	parts := []virtual.Part[[]tf.ProviderVersion[virtualProviderVersion]]{}
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if !providerMember(c) {
			continue
		}
		if !q.isList && !q.isMirror {
			doc, reads, paths, e := h.virtualProviderDownload(r, mr, q)
			if e != nil {
				if modulePassedOver(e) {
					continue
				}
				h.k.Error(w, r, e)
				return
			}
			h.serveProviderJSON(w, r, doc, reads, paths)
			return
		}
		versions, e := h.virtualProviderPart(r, mr, q)
		if e != nil {
			if modulePassedOver(e) {
				continue
			}
			h.k.Error(w, r, e)
			return
		}
		if !q.isList {
			for _, version := range versions {
				if version.Version != q.version {
					continue
				}
				doc, reads, paths, e := h.virtualMirrorVersion(r, q, version.Value)
				if e != nil {
					if modulePassedOver(e) {
						continue
					}
					h.k.Error(w, r, e)
					return
				}
				h.serveProviderJSON(w, r, doc, reads, paths)
				return
			}
			continue
		}
		parts = append(parts, virtual.Part[[]tf.ProviderVersion[virtualProviderVersion]]{Member: c, Doc: versions})
	}
	if !q.isList {
		h.k.Error(w, r, missing())
		return
	}
	h.virtualProviderList(w, r, q, parts)
}

func (h handler) virtualProviderList(w http.ResponseWriter, r *http.Request, q providerOp, parts []virtual.Part[[]tf.ProviderVersion[virtualProviderVersion]]) {
	for {
		versions, err := (tf.ProviderMerger[virtualProviderVersion]{}).Merge(r.Context(), parts)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if len(versions) == 0 {
			h.k.Error(w, r, missing())
			return
		}
		rows := []remoteProviderVersion{}
		mirror := map[string]any{}
		for _, v := range versions {
			rows = append(rows, v.Value.wire)
			mirror[v.Version] = struct{}{}
		}
		var doc any = map[string]any{"versions": rows}
		if q.isMirror {
			doc = map[string]any{"versions": mirror}
		}
		// Rendering precedes the last ownership fence; a failed retained owner
		// is removed before the union is recomputed and a later owner selected.
		if _, err := json.Marshal(doc); err != nil {
			h.k.Error(w, r, fmt.Errorf("terraform provider response: %w", err))
			return
		}
		retry := false
		reads := []source.Result{}
		for _, v := range versions {
			if err := h.virtualProviderVersionFence(r, v.Value); err != nil {
				if !modulePassedOver(err) {
					h.k.Error(w, r, err)
					return
				}
				owner := pkgroute.RequestFrom(v.Value.member).Repo.Key
				for i := range parts {
					if parts[i].Member.Key == owner {
						parts[i].Doc = slices.DeleteFunc(parts[i].Doc, func(candidate tf.ProviderVersion[virtualProviderVersion]) bool { return candidate.Version == v.Version })
					}
				}
				retry = true
				continue
			}
			reads = append(reads, v.Value.reads...)
			value := v.Value
			reads = append(reads, source.Result{Revalidate: func(ctx context.Context) error { return h.virtualProviderVersionFence(r.WithContext(ctx), value) }})
		}
		if retry {
			continue
		}
		h.serveProviderJSON(w, r, doc, reads, nil)
		return
	}
}

func (h handler) virtualProviderDownload(r, mr *http.Request, q providerOp) (any, []source.Result, []disclosed, error) {
	if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
		return h.virtualRemoteProviderDownload(r, mr, q)
	}
	if !h.k.CanFolder(mr, "", api.ActionRead) {
		return nil, nil, nil, missing()
	}
	versions, err := h.providerVersions(mr, q.name, false, false)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, v := range versions {
		if v.version != q.version {
			continue
		}
		for _, a := range v.archives {
			if a.os != q.os || a.arch != q.arch {
				continue
			}
			items := []model.Item{a.item, v.sums, v.signature}
			paths := []string{a.item.FullPath(), v.sums.FullPath(), v.signature.FullPath()}
			if err := h.virtualProviderAdmit(r, mr, paths); err != nil {
				if modulePassedOver(err) {
					continue
				}
				return nil, nil, nil, err
			}
			// Optional protocol metadata obeys the same visibility as the
			// version list; a hidden manifest cannot reappear in a descriptor.
			if v.manifest.ID != 0 {
				allowed, err := h.virtualProviderAllowed(r, mr, v.manifest.FullPath())
				if err != nil {
					return nil, nil, nil, err
				}
				if allowed {
					paths = append(paths, v.manifest.FullPath())
				} else {
					v.manifest = model.Item{}
				}
			}
			doc, err := h.providerDownloadData(mr, v, a)
			if err != nil {
				return nil, nil, nil, err
			}
			for index, key := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
				doc[key], err = h.virtualLocalProviderURL(r, mr, q, items[index], index)
				if err != nil {
					return nil, nil, nil, err
				}
			}
			if err := h.virtualProviderFileFence(r, mr, paths); err != nil {
				return nil, nil, nil, err
			}
			read := source.Result{Revalidate: func(ctx context.Context) error { return h.virtualProviderFileFence(r.WithContext(ctx), mr, paths) }}
			return doc, []source.Result{read}, nil, nil
		}
	}
	return nil, nil, nil, missing()
}

func (h handler) virtualRemoteProviderDownload(r, mr *http.Request, q providerOp) (any, []source.Result, []disclosed, error) {
	if !h.k.CanFolder(mr, providerFolder(q), api.ActionRead) {
		return nil, nil, nil, missing()
	}
	if err := h.k.CheckDocument(mr, providerDocument(q)); err != nil {
		return nil, nil, nil, fmt.Errorf("terraform provider metadata policy: %w", err)
	}
	versions, index, err := h.upstreamProviderVersions(mr, q)
	if err != nil {
		return nil, nil, nil, err
	}
	if !slices.ContainsFunc(versions, func(v remoteProviderVersion) bool { return v.Version == q.version }) {
		return nil, nil, nil, missing()
	}
	bundle, err := h.providerBundle(mr, q, func(paths []string) error { return h.virtualProviderAdmit(r, mr, paths) })
	defer bundle.close()
	if err != nil {
		return nil, nil, nil, err
	}
	doc := map[string]json.RawMessage{}
	for key, value := range bundle.metadata.doc {
		doc[key] = value
	}
	for index, key := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
		target, err := h.virtualRemoteProviderURL(r, mr, bundle, index)
		if err != nil {
			return nil, nil, nil, err
		}
		doc[key], _ = json.Marshal(target)
	}
	reads := append([]source.Result{index}, bundle.results...)
	if err := combineModuleFences(reads, permissionCheck, bundle.disclosed())(r.Context()); err != nil {
		return nil, nil, nil, err
	}
	if err := h.virtualProviderFileFence(r, mr, bundle.paths); err != nil {
		return nil, nil, nil, err
	}
	paths := append([]string(nil), bundle.paths...)
	reads = append(reads, source.Result{Revalidate: func(ctx context.Context) error { return h.virtualProviderFileFence(r.WithContext(ctx), mr, paths) }})
	return doc, reads, bundle.disclosed(), nil
}

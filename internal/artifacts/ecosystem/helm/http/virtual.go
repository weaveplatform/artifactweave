package helmhttp

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	helmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
)

func (h handler) virtual(w http.ResponseWriter, r *http.Request, p string) {
	h.virtualRead = true
	if p != helmfmt.IndexPath {
		h.virtualFile(w, r, p)
		return
	}
	c := pkgroute.RequestFrom(r).Repo
	namespaced := c.UseNamespaces != nil && *c.UseNamespaces
	e, err := h.k.Merged(r, helmfmt.IndexPath, virtual.PartOf(helmfmt.IndexPath, fmt.Sprintf("%t:%t", h.settings.IsRelative(), namespaced)), h.mergeIndex)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.k.ServeResult(w, r, source.Result{Stream: &source.Stream{
		Body: &remoteIndexBody{Reader: bytes.NewReader(e.Body)}, Status: http.StatusOK,
		Representation: source.Representation{MediaType: "application/x-yaml", Size: int64(len(e.Body)), ETag: e.Header.Get("ETag")},
	}})
}

func (h handler) mergeIndex(r *http.Request) (virtual.Entry, error) {
	members, err := h.k.MergeMembers(r, helmfmt.IndexPath)
	if err != nil {
		return virtual.Entry{}, fmt.Errorf("helm members: %w", err)
	}
	var parts []virtual.Part[helmfmt.Index]
	for _, mr := range members {
		index, err := h.memberIndex(mr)
		if err != nil {
			if !pkgroute.PassedOver(err) {
				return virtual.Entry{}, err
			}
			h.k.WarnPassedOver(mr, err)
			continue
		}
		parts = append(parts, virtual.Part[helmfmt.Index]{Member: pkgroute.RequestFrom(mr).Repo, Doc: index})
	}
	if len(parts) == 0 {
		return virtual.Entry{}, apierror.New(http.StatusNotFound, "Could not find resource")
	}
	c := pkgroute.RequestFrom(r).Repo
	index, err := (helmfmt.Merger{Settings: h.settings, Base: baseurl.From(r.Context()), Key: c.Key, UseNamespaces: c.UseNamespaces != nil && *c.UseNamespaces}).Merge(
		r.Context(),
		parts,
	)
	if err != nil {
		return virtual.Entry{}, fmt.Errorf("helm merge: %w", err)
	}
	body, err := helmfmt.EncodeIndex(index)
	if err != nil {
		return virtual.Entry{}, fmt.Errorf("helm virtual index: %w", err)
	}
	return virtual.Entry{Body: body, Header: http.Header{"Content-Type": {"application/x-yaml"}, "Etag": {fmt.Sprintf(`"%x"`, sha256.Sum256(body))}}}, nil
}

func (h handler) memberIndex(r *http.Request) (helmfmt.Index, error) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		index, result, err := h.upstreamIndex(r, helmfmt.IndexPath)
		if err != nil {
			return helmfmt.Index{}, err
		}
		if result.Revalidate != nil {
			if err = result.Revalidate(r.Context()); err != nil {
				return helmfmt.Index{}, fmt.Errorf("helm member authority: %w", err)
			}
		}
		var out helmfmt.Index
		if err = index.node.Decode(&out); err != nil {
			return helmfmt.Index{}, remoteError()
		}
		out.Entries = map[string][]helmfmt.ChartVersion{}
		for name, versions := range index.versions {
			for _, version := range versions {
				chart := version.chart
				chart.URLs = nil
				for _, target := range version.targets {
					chart.URLs = append(chart.URLs, (&url.URL{Path: target.path}).String())
				}
				if len(chart.URLs) > 0 {
					out.Entries[name] = append(out.Entries[name], chart)
				}
			}
		}
		return out, nil
	}
	f, err := h.k.ResolveStoredItem(r, helmfmt.IndexPath)
	if err != nil {
		return helmfmt.Index{}, fmt.Errorf("helm member index: %w", err)
	}
	body, _, err := h.k.Blobs().Open(r.Context(), f.Item.Actual.SHA256)
	if err != nil {
		return helmfmt.Index{}, fmt.Errorf("helm member index: %w", err)
	}
	defer body.Close()
	var out helmfmt.Index
	if err = yaml.NewDecoder(io.LimitReader(body, remoteIndexLimit+1)).Decode(&out); err != nil {
		return helmfmt.Index{}, fmt.Errorf("helm stored index: %w", err)
	}
	return out, nil
}

func (h handler) virtualFile(w http.ResponseWriter, r *http.Request, p string) {
	h.virtualRead = true
	member, actual, _, err := h.namespace(r, p)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	h.virtualMemberFile(w, r, actual, member)
}

func (h handler) namespace(r *http.Request, p string) (string, string, bool, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.UseNamespaces == nil || !*c.UseNamespaces {
		return "", p, false, nil
	}
	members, err := virtual.Members(r.Context(), h.k.DB().Repos(), c, virtual.Options{Order: virtual.Package})
	if err != nil {
		return "", "", false, fmt.Errorf("helm namespaces: %w", err)
	}
	member, actual, named := helmfmt.NamespacePath(c, members, p)
	return member, actual, named, nil
}

func (h handler) virtualMemberFile(w http.ResponseWriter, r *http.Request, p, member string) {
	who, _ := kernel.FromContext(r.Context())
	members, err := h.k.MergeMembers(r, p)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if member != "" && pkgroute.RequestFrom(mr).Repo.Key != member {
			continue
		}
		if !h.k.Can(mr, p, api.ActionRead) {
			continue
		}
		if owner, prov := strings.CutSuffix(p, ".prov"); prov && !h.k.Can(mr, owner, api.ActionRead) {
			continue
		}
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			result, err := h.remoteChart(mr, p)
			if err == nil {
				if h.item != nil && result.Stream != nil {
					c := pkgroute.RequestFrom(mr).Repo
					h.item.ServeRemoteStream(w, mr, result, c, itemkit.ItemRequest{Key: c.Key, Path: p}, source.Resource{}, "", who.Anonymous)
					return
				}
				h.k.ServeResult(w, mr, result)
				return
			}
			if !pkgroute.PassedOver(err) {
				h.k.Error(w, r, err)
				return
			}
			h.k.WarnPassedOver(mr, err)
		} else {
			f, err := h.k.DownloadItem(mr, p)
			if err == nil && !f.Item.Folder {
				h.k.ServeItem(w, mr, p)
				return
			}
			if err != nil && !pkgroute.PassedOver(err) {
				h.k.Error(w, r, err)
				return
			}
		}
		h.k.EvictMerged(r, helmfmt.IndexPath)
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
}

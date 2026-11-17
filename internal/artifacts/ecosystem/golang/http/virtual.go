package golanghttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	goformat "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/golang"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type memberList struct {
	lines  []string
	result source.Result
}

func (h reads) virtual(w http.ResponseWriter, r *http.Request, q goformat.Request) {
	if q.Kind != goformat.KindList && q.Kind != goformat.KindLatest {
		h.virtualFile(w, r, q.File())
		return
	}
	lists, err := h.virtualLists(r, q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	parts := make([]virtual.Part[[]string], 0, len(lists))
	for _, p := range lists {
		parts = append(parts, virtual.Part[[]string]{Member: p.Member, Doc: p.Doc.lines})
	}
	lines, err := (goformat.Merger{}).Merge(r.Context(), parts)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(lines) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound, fmt.Sprintf("Module %s has no version in %s.", q.Module, pkgroute.RequestFrom(r).Repo.Key)))
		return
	}
	if q.Kind == goformat.KindLatest {
		versions := make([]string, 0, len(lines))
		for _, line := range lines {
			versions = append(versions, goformat.LineVersion(line))
		}
		latest, _ := goformat.Latest(versions)
		h.virtualFile(w, r, goformat.File{Module: q.Module, Version: latest, Ext: goformat.ExtInfo})
		return
	}
	body := strings.Join(lines, "\n") + "\n"
	result := source.Result{Stream: &source.Stream{
		Body: io.NopCloser(strings.NewReader(body)), Status: http.StatusOK,
		Representation: source.Representation{MediaType: "text/plain; charset=utf-8", Size: int64(len(body))},
	}}
	result.Revalidate = func(ctx context.Context) error {
		for _, p := range lists {
			if p.Doc.result.Revalidate != nil {
				if err := p.Doc.result.Revalidate(ctx); err != nil {
					return fmt.Errorf("go virtual delivery: %w", err)
				}
			}
		}
		return nil
	}
	result.Used = func(ctx context.Context) error {
		for _, p := range lists {
			if p.Doc.result.Used != nil {
				if err := p.Doc.result.Used(ctx); err != nil {
					return fmt.Errorf("go virtual delivery: %w", err)
				}
			}
		}
		return nil
	}
	h.k.ServeResult(w, r, result)
}

func (h reads) virtualLists(r *http.Request, q goformat.Request) ([]virtual.Part[memberList], error) {
	requested := q.Module + "/@v/list"
	if q.Kind == goformat.KindLatest {
		requested = q.Module + "/@latest"
		if strings.HasSuffix(pkgroute.RequestFrom(r).Rest, "/@v/latest.info") {
			requested = q.Module + "/@v/latest.info"
		}
	}
	parts, err := pkgroute.Collect(h.k, r, requested, func(mr *http.Request) (memberList, error) {
		list, err := h.memberList(mr, q.Module)
		if err != nil {
			h.k.WarnPassedOver(mr, err)
			return list, err
		}
		visible := list.lines[:0]
		for _, line := range list.lines {
			path := (goformat.File{Module: q.Module, Version: goformat.LineVersion(line), Ext: goformat.ExtMod}).Path()
			if h.k.Can(mr, path, api.ActionRead) && h.k.CheckDownload(mr, path) == nil {
				visible = append(visible, line)
			}
		}
		list.lines = visible
		return list, nil
	})
	if err != nil {
		return nil, fmt.Errorf("go virtual lists: %w", err)
	}
	return parts, nil
}

func (h reads) memberList(r *http.Request, module string) (memberList, error) {
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		versions, err := h.readable(r, module)
		if err != nil {
			return memberList{}, fmt.Errorf("go member list: %w", err)
		}
		indexed := make([]model.PackageVersion, 0, len(versions))
		for _, v := range versions {
			indexed = append(indexed, v.PackageVersion)
		}
		return memberList{lines: goformat.Listed(indexed)}, nil
	}
	resource, err := goformat.GoResource(module+"/@v/list", http.MethodGet)
	if err != nil {
		return memberList{}, fmt.Errorf("go member list: %w", err)
	}
	result, err := h.k.ReadMetadata(r, resource)
	if err != nil {
		return memberList{}, fmt.Errorf("go member list: %w", err)
	}
	defer result.Stream.Body.Close()
	data, err := pkgroute.ReadServed(r.Context(), result.Stream.Body, 4<<20, context.Canceled)
	if err != nil {
		return memberList{}, fmt.Errorf("go member list: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	nonempty := lines[:0]
	for _, line := range lines {
		if goformat.LineVersion(line) != "" {
			nonempty = append(nonempty, line)
		}
	}
	return memberList{lines: nonempty, result: result}, nil
}

func (h reads) virtualFile(w http.ResponseWriter, r *http.Request, f goformat.File) {
	members, err := h.k.MergeMembers(r, f.Path())
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, mr := range members {
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			resource, resourceErr := goformat.GoResource(f.Path(), r.Method)
			if resourceErr != nil {
				h.k.Error(w, r, resourceErr)
				return
			}
			result, readErr := h.k.Read(mr, resource)
			if readErr == nil {
				h.k.ServeResult(w, mr, result)
				return
			}
			if !pkgroute.PassedOver(readErr) {
				h.k.Error(w, r, readErr)
				return
			}
			h.k.WarnPassedOver(mr, readErr)
			continue
		}
		v, readErr := h.localVersion(mr, f)
		if readErr == nil {
			if f.Ext == goformat.ExtInfo {
				h.serveInfo(w, mr, f.Module, v)
			} else {
				h.k.ServeItem(w, mr, f.Path())
			}
			return
		}
		if !pkgroute.PassedOver(readErr) {
			h.k.Error(w, r, readErr)
			return
		}
		h.k.WarnPassedOver(mr, readErr)
	}
	h.k.Error(w, r, apierror.New(http.StatusNotFound, fmt.Sprintf("Version %s of module %s is not in %s.", f.Version, f.Module, pkgroute.RequestFrom(r).Repo.Key)))
}

func (h reads) localVersion(r *http.Request, f goformat.File) (version, error) {
	v, err := h.version(r, f.Module, f.Version)
	if err != nil {
		return v, err
	}
	permission := f.Path()
	if f.Ext == goformat.ExtInfo && !v.hasInfo {
		permission = (goformat.File{Module: f.Module, Version: f.Version, Ext: goformat.ExtMod}).Path()
	}
	if !h.k.Can(r, permission, api.ActionRead) {
		return version{}, kernel.ErrNotFound
	}
	if err := h.k.CheckDownload(r, f.Path()); err != nil {
		return version{}, fmt.Errorf("go member version: %w", err)
	}
	return v, nil
}

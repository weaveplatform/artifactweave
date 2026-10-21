package terraformbackend

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (h handler) account(_ http.ResponseWriter, r *http.Request) (result, error) {
	return document(200, resource("users", caller(r), map[string]any{"username": caller(r)})), nil
}

func (h handler) organization(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, err := h.repository(r)
	if err == nil {
		err = require(r, c, "", types.ActionRead, true)
	}
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	return document(200, resource("organizations", c.Key, map[string]any{"name": c.Key})), nil
}

func (h handler) entitlements(w http.ResponseWriter, r *http.Request) (result, error) {
	if _, err := h.organization(w, r); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	return document(200, resource("entitlement-sets", parameter(r, "repo"), map[string]any{
		"state-storage": true, "operations": false, "private-module-registry": false,
		"sentinel": false, "teams": false, "vcs-integrations": false, "cost-estimation": false,
	})), nil
}

func workspaceResource(ws terraformstate.Workspace) map[string]any {
	data := resource("workspaces", ws.ID(), map[string]any{
		"name":              ws.Name,
		"locked":            ws.Lock != nil,
		"execution-mode":    "local",
		"operations":        false,
		"terraform-version": ws.TerraformVersion,
		"created-at":        ws.Created.Format(time.RFC3339Nano),
		"updated-at":        ws.Updated.Format(time.RFC3339Nano),
	})
	data["relationships"] = map[string]any{
		"organization": map[string]any{
			"data": map[string]string{"id": ws.Repo, "type": "organizations"},
		},
	}
	return data
}

func (h handler) workspace(_ http.ResponseWriter, r *http.Request) (result, error) {
	_, ws, err := h.resolveWorkspace(r, types.ActionRead)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	return document(200, workspaceResource(ws)), nil
}

func (h handler) workspaces(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, err := h.repository(r)
	if err == nil {
		err = require(r, c, "", types.ActionRead, true)
	}
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	all, err := h.states.Workspaces(r.Context(), c)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	slices.SortFunc(
		all,
		func(a, b terraformstate.Workspace) int { return cmp.Compare(a.Name, b.Name) },
	)
	rows := make([]any, 0, len(all))
	for _, ws := range all {
		if strings.Contains(ws.Name, r.URL.Query().Get("search[name]")) &&
			can(r, c, tfbe.Folder(ws.Name), types.ActionRead, true) {
			rows = append(rows, workspaceResource(ws))
		}
	}
	return paginated(r, rows)
}

func (h handler) createWorkspace(w http.ResponseWriter, r *http.Request) (result, error) {
	c, err := h.repository(r)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	var input struct {
		Data struct {
			Type       string `json:"type"`
			Attributes struct {
				Name    string `json:"name"`
				Version string `json:"terraform-version"` //nolint:tagliatelle // go-tfe wire name
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := decode(w, r, &input); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	if input.Data.Type != "workspaces" {
		return result{}, apierror.New(400, "Expected a workspaces document")
	}
	a := input.Data.Attributes
	if err := tfbe.CheckName(a.Name); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	if err := require(r, c, tfbe.RecordPath(a.Name), types.ActionWrite, false); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	ws, err := h.states.CreateWorkspace(r.Context(), c, a.Name, a.Version, caller(r))
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	return document(201, workspaceResource(ws)), nil
}

func (h handler) deleteWorkspace(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, ws, err := h.resolveWorkspace(r, types.ActionDelete)
	if err == nil {
		err = h.states.DeleteWorkspace(r.Context(), c, ws.Name, caller(r))
	}
	return result{
		status: 204,
	}, err //nolint:wrapcheck // storage errors retain their typed API status
}

func (h handler) lock(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, ws, err := h.resolveWorkspace(r, types.ActionRead)
	if err == nil {
		ws, err = h.states.Lock(r.Context(), c, ws.Name, caller(r))
	}
	return document(200, workspaceResource(ws)), err
}

func (h handler) unlock(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, ws, err := h.resolveWorkspace(r, types.ActionRead)
	if err == nil {
		ws, err = h.states.Unlock(r.Context(), c, ws.Name, caller(r))
	}
	return document(200, workspaceResource(ws)), err
}

func (h handler) forceUnlock(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, ws, err := h.resolveWorkspace(r, types.ActionDelete)
	if err == nil {
		ws, err = h.states.ForceUnlock(r.Context(), c, ws.Name)
	}
	return document(200, workspaceResource(ws)), err
}

func paginated(r *http.Request, rows []any) (result, error) {
	page, size := 1, 20
	for _, v := range []struct {
		key  string
		into *int
	}{{"page[number]", &page}, {"page[size]", &size}} {
		if raw := r.URL.Query().Get(v.key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return result{}, apierror.New(400, "Invalid pagination")
			}
			*v.into = n
		}
	}
	size = min(size, 100)
	totalPages := (len(rows) + size - 1) / size
	start := len(rows)
	if page <= totalPages {
		start = (page - 1) * size
	}
	end := min(start+size, len(rows))
	prev, next := 0, 0
	if page > 1 {
		prev = page - 1
	}
	if page < totalPages {
		next = page + 1
	}
	return result{
		200,
		map[string]any{"data": rows[start:end], "meta": map[string]any{"pagination": map[string]int{
			"current-page": page,
			"prev-page":    prev,
			"next-page":    next,
			"total-pages":  totalPages,
			"total-count":  len(rows),
		}}},
	}, nil
}

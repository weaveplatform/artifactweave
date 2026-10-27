// Package terraformbackend serves the TFE-shaped API used by Terraform's
// remote backend (decision 0041). Repository items remain its source of truth.
package terraformbackend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/crypt"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Prefix is the service URL advertised by Terraform discovery.
const (
	Prefix    = "/artifactory/api/terraform/remote/v2"
	mediaType = "application/vnd.api+json"
	bodyLimit = 64 << 20
)

// Options supplies the shared item service, keyring and fault recorder.
// Now is a test clock; nil uses the wall clock.
type Options struct {
	Items *item.Service
	Keys  *crypt.Keyring
	Now   func() time.Time
	Fault func(*http.Request, error)
}

type (
	handler struct {
		o      Options
		states *terraformstate.Service
	}
	result struct {
		status int
		body   any
	}
	endpoint func(http.ResponseWriter, *http.Request) (result, error)
)

// IsPath reports whether a request belongs to this API, including its root.
func IsPath(path string) bool { return path == Prefix || strings.HasPrefix(path, Prefix+"/") }

// SkipsAuthentication admits only the probe and the capability upload.
// Checking the full route keeps a query token from bypassing another API.
func SkipsAuthentication(r *http.Request) bool {
	if r.Method == http.MethodGet && r.URL.EscapedPath() == Prefix+"/ping" {
		return true
	}
	return r.Method == http.MethodPut && uploadPath(r.URL.EscapedPath())
}

func uploadPath(path string) bool {
	rest, ok := strings.CutPrefix(path, Prefix+"/state-versions/")
	id, end, found := strings.Cut(rest, "/")
	return ok && found && id != "" && end == "content"
}

// Register mounts the API with explicit chi routes. The backend resolves
// workspace IDs against both repository and workspace existence; the registry
// router's repository-only resolution would choose the wrong ambiguous ID.
func Register(r chi.Router, o Options) {
	if o.Now == nil {
		o.Now = time.Now
	}
	h := handler{o: o, states: &terraformstate.Service{Items: o.Items, Keys: o.Keys, Now: o.Now}}
	r.Route(Prefix, func(r chi.Router) {
		missing := func(w http.ResponseWriter, r *http.Request) {
			h.error(w, r, apierror.New(404, "Terraform backend operation not found"))
		}
		r.NotFound(missing)
		r.MethodNotAllowed(missing)
		r.Get(
			"/ping",
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		)
		r.Get("/account/details", h.wrap(h.account))
		r.Get("/organizations/{repo}", h.wrap(h.organization))
		r.Get("/organizations/{repo}/entitlement-set", h.wrap(h.entitlements))
		r.Get("/organizations/{repo}/workspaces", h.wrap(h.workspaces))
		r.Post("/organizations/{repo}/workspaces", h.wrap(h.createWorkspace))
		r.Get("/organizations/{repo}/workspaces/{name}", h.wrap(h.workspace))
		r.Delete("/organizations/{repo}/workspaces/{name}", h.wrap(h.deleteWorkspace))
		r.Get("/workspaces/{id}", h.wrap(h.workspace))
		r.Delete("/workspaces/{id}", h.wrap(h.deleteWorkspace))
		r.Post("/workspaces/{id}/actions/lock", h.wrap(h.lock))
		r.Post("/workspaces/{id}/actions/unlock", h.wrap(h.unlock))
		r.Post("/workspaces/{id}/actions/force-unlock", h.wrap(h.forceUnlock))
		r.Get("/workspaces/{id}/current-state-version", h.wrap(h.currentState))
		r.Post("/workspaces/{id}/state-versions", h.wrap(h.createState))
		r.Get("/state-versions", h.wrap(h.statesList))
		r.Get("/state-versions/{id}", h.wrap(h.state))
		r.Get("/state-versions/{id}/download", h.wrap(h.download))
		r.Put("/state-versions/{id}/content", h.wrap(h.upload))
	})
}

func (h handler) wrap(f endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !SkipsAuthentication(r) {
			if _, err := auth.RequireAuthenticated(r.Context()); err != nil {
				h.error(w, r, err)
				return
			}
		}
		if h.o.Items == nil || h.o.Items.Store == nil {
			h.error(w, r, apierror.ErrNotImplemented)
			return
		}
		out, err := f(w, r)
		if err != nil {
			h.error(w, r, err)
			return
		}
		if out.status == 0 {
			return
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(out.status)
		if out.body != nil {
			_ = json.NewEncoder(w).Encode(out.body)
		}
	}
}

func (h handler) error(w http.ResponseWriter, r *http.Request, err error) {
	var locked *terraformstate.LockedError
	var unlocked *terraformstate.NotLockedError
	var invalid *terraformstate.InvalidStateError
	var large *http.MaxBytesError
	switch {
	case errors.Is(err, terraformstate.ErrWorkspaceChanged):
		err = forbidden
	case errors.Is(err, store.ErrNotFound):
		err = apierror.New(404, err.Error())
	case errors.Is(err, terraformstate.ErrTaken),
		errors.Is(err, terraformstate.ErrInvalidName),
		errors.As(err, &invalid):
		err = apierror.New(422, err.Error())
	case errors.Is(err, terraformstate.ErrWritten),
		errors.As(err, &locked),
		errors.As(err, &unlocked):
		err = apierror.New(409, err.Error())
	case errors.As(err, &large):
		err = apierror.New(413, "Request body is too large")
	}
	var typed *apierror.Error
	if !errors.As(err, &typed) && h.o.Fault != nil {
		h.o.Fault(r, err)
	}
	apierror.Write(w, apierror.SurfaceTerraformBackend, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err := d.Decode(v); err != nil {
		return decodeError(err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return apierror.New(400, "Expected one JSON:API document")
		}
		return decodeError(err)
	}
	return nil
}

func decodeError(err error) error {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		return err
	}
	return apierror.New(400, "Invalid JSON:API document")
}

func parameter(r *http.Request, name string) string {
	s, _ := url.PathUnescape(chi.URLParam(r, name))
	return s
}

func can(r *http.Request, c repo.Config, path string, action types.Actions, folder bool) bool {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Anonymous || p.Access() == nil {
		return false
	}
	target := authz.Repo{Key: c.Key, Class: string(c.Class)}
	if folder {
		return p.Access().AllowsFolder(types.ResourceArtifact, target, path, action)
	}
	return p.Access().Allows(types.ResourceArtifact, target, path, action)
}

func require(r *http.Request, c repo.Config, path string, action types.Actions, folder bool) error {
	if !can(r, c, path, action, folder) {
		return auth.ErrForbidden
	}
	return nil
}
func caller(r *http.Request) string { p, _ := auth.FromContext(r.Context()); return p.Name }
func (h handler) repository(r *http.Request) (repo.Config, error) {
	c, err := h.states.Repository(r.Context(), parameter(r, "repo"))
	if err != nil {
		return c, fmt.Errorf("terraformbackend: organization: %w", err)
	}
	return c, nil
}

func (h handler) resolveWorkspace(
	r *http.Request,
	action types.Actions,
) (repo.Config, terraformstate.Workspace, error) {
	var c repo.Config
	var ws terraformstate.Workspace
	var err error
	if id := parameter(r, "id"); id != "" {
		c, ws, err = h.states.ResolveWorkspace(r.Context(), id)
	} else {
		c, err = h.repository(r)
		if err == nil {
			ws, err = h.states.Workspace(r.Context(), c, parameter(r, "name"))
		}
	}
	if err == nil {
		err = require(r, c, tfbe.Folder(ws.Name), action, true)
	}
	return c, ws, err
}

func resource(kind, id string, attrs map[string]any) map[string]any {
	return map[string]any{"type": kind, "id": id, "attributes": attrs}
}
func document(status int, data any) result { return result{status, map[string]any{"data": data}} }

package terraformbackend

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/internal/service/terraformstate"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func stateResource(r *http.Request, st terraformstate.State, upload string) map[string]any {
	status := "finalized"
	attrs := map[string]any{
		"serial":     st.Meta.Serial,
		"size":       st.Meta.Size,
		"created-at": st.Created().Format(time.RFC3339Nano),
		"hosted-state-download-url": baseurl.From(
			r.Context(),
		) + Prefix + "/state-versions/" + url.PathEscape(
			st.ID(),
		) + "/download",
	}
	if upload != "" {
		status = "pending"
		attrs["hosted-state-upload-url"] = upload
	}
	attrs["status"] = status
	return resource("state-versions", st.ID(), attrs)
}

func (h handler) currentState(_ http.ResponseWriter, r *http.Request) (result, error) {
	c, ws, err := h.resolveWorkspace(r, types.ActionRead)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	st, err := h.states.CurrentState(r.Context(), c, ws.Name)
	return document(200, stateResource(r, st, "")), err
}

func (h handler) resolveState(r *http.Request) (repo.Config, terraformstate.State, error) {
	c, st, err := h.states.ResolveState(r.Context(), parameter(r, "id"))
	if err == nil {
		err = require(r, c, tfbe.Folder(st.Workspace), types.ActionRead, true)
	}
	return c, st, err
}

func (h handler) state(_ http.ResponseWriter, r *http.Request) (result, error) {
	_, st, err := h.resolveState(r)
	return document(200, stateResource(r, st, "")), err
}

func (h handler) download(w http.ResponseWriter, r *http.Request) (result, error) {
	c, st, err := h.resolveState(r)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	plain, _, err := h.states.ReadState(r.Context(), c, st.Workspace, st.Ms)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	// State is one authenticated encryption message: complete verification
	// precedes headers, so a corrupt seal never produces partial plaintext.
	_, _ = w.Write(plain) //nolint:gosec // application/json state, never an HTML response
	return result{}, nil
}

func (h handler) statesList(_ http.ResponseWriter, r *http.Request) (result, error) {
	key, name := r.URL.Query().
		Get("filter[organization][name]"),
		r.URL.Query().
			Get("filter[workspace][name]")
	if key == "" || name == "" {
		return result{}, apierror.New(400, "Both organization and workspace filters are required")
	}
	c, err := h.states.Repository(r.Context(), key)
	if err == nil {
		err = require(r, c, tfbe.Folder(name), types.ActionRead, true)
	}
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	states, err := h.states.States(r.Context(), c, name)
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	rows := make([]any, 0, len(states))
	for _, st := range states {
		rows = append(rows, stateResource(r, st, ""))
	}
	return paginated(r, rows)
}

type stateInput struct {
	Data struct {
		Type       string `json:"type"`
		Attributes struct {
			MD5    string  `json:"md5"`
			Serial *int64  `json:"serial"`
			State  *string `json:"state"`
		} `json:"attributes"`
	} `json:"data"`
}

func (h handler) createState(w http.ResponseWriter, r *http.Request) (result, error) {
	// A writer need not have Read: the table grants Deploy on the latest
	// state. The client normally also needs Read for init and locking.
	c, ws, err := h.states.ResolveWorkspace(r.Context(), parameter(r, "id"))
	if err == nil {
		err = require(r, c, tfbe.LatestPath(ws.Name), types.ActionWrite, false)
	}
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	var input stateInput
	if err := decode(w, r, &input); err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	if input.Data.Type != "state-versions" {
		return result{}, apierror.New(400, "Expected a state-versions document")
	}
	a := input.Data.Attributes
	digest, err := hex.DecodeString(a.MD5)
	if err != nil || len(digest) != 16 || a.Serial == nil || *a.Serial < 0 {
		return result{}, apierror.New(422, "A valid md5 and non-negative serial are required")
	}
	var st terraformstate.State
	if a.State != nil {
		plain, err := base64.StdEncoding.DecodeString(*a.State)
		if err != nil {
			return result{}, apierror.New(422, "State must be base64 encoded")
		}
		st, err = h.states.WriteState(
			r.Context(),
			c,
			ws.Name,
			terraformstate.StateWrite{
				Body:   bytes.NewReader(plain),
				MD5:    a.MD5,
				Serial: *a.Serial,
				By:     caller(r),
			},
		)
		return document(201, stateResource(r, st, "")), err
	}
	if h.o.Keys == nil {
		return result{}, apierror.ErrNotImplemented
	}
	ms, err := h.states.NewState(r.Context(), c, ws.Name, caller(r))
	if err != nil {
		return result{}, err //nolint:wrapcheck // storage errors retain their typed API status
	}
	st = terraformstate.State{
		Repo:      c.Key,
		Workspace: ws.Name,
		Ms:        ms,
		Meta:      tfbe.Meta{Serial: *a.Serial},
	}
	p, _ := auth.FromContext(r.Context())
	upload, err := h.uploadURL(r, st, ws, p, strings.ToLower(a.MD5))
	return document(201, stateResource(r, st, upload)), err
}

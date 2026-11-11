package bowerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/bower"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// stashPointer treats the documented SSH address as a Git locator, never an
// executable transport. The operator's HTTP origin supplies port, context and
// credentials; the locator supplies only a same-host project/repository pair.
func stashPointer(c repo.Config, name, raw string) (gitPointer, error) {
	if c.VCSGitProvider != repo.Stash && c.VCSGitProvider != repo.OldStash {
		return gitPointer{}, source.ErrUnsupported
	}
	base, err := configuredURL(c.URL)
	if err != nil {
		return gitPointer{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "ssh" || u.User == nil || u.User.String() != "git" ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		strings.Contains(u.EscapedPath(), "%") {
		return gitPointer{}, source.ErrInvalidRequest
	}
	if !strings.EqualFold(u.Hostname(), base.Hostname()) {
		return gitPointer{}, source.ErrDenied
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return gitPointer{}, source.ErrInvalidRequest
		}
	}
	parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || !strings.HasSuffix(u.Path, ".git") || !bowerfmt.ValidName(parts[0]) ||
		!bowerfmt.ValidName(parts[1]) {
		return gitPointer{}, source.ErrInvalidRequest
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/scm/" + parts[0] + "/" + parts[1] + ".git"
	base.RawPath = ""
	return gitPointer{name: name, owner: parts[0], project: parts[1], url: base}, nil
}

func (h handler) register(w http.ResponseWriter, r *http.Request) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class != repo.Remote && c.Class != repo.Virtual {
		h.k.Error(
			w,
			r,
			apierror.New(
				http.StatusMethodNotAllowed,
				"Bower registration requires a remote or virtual repository",
			),
		)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower registration"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(data) > 8192 {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower registration"))
		return
	}
	form, err := url.ParseQuery(string(data))
	if err != nil || len(form) != 2 || len(form["name"]) != 1 || len(form["url"]) != 1 ||
		!bowerfmt.ValidName(form.Get("name")) {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower registration"))
		return
	}
	name, raw := form.Get("name"), form.Get("url")
	if !h.k.RequireFolder(w, r, name, api.ActionWrite) {
		return
	}
	target, err := h.registrationTarget(r, name, raw)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	if !h.k.RequireFolder(w, target, name, api.ActionWrite) {
		return
	}
	selected := pkgroute.RequestFrom(target).Repo
	identity, err := h.k.DB().RemoteCache().Source(r.Context(), selected.Key)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	err = h.k.DB().Tx(r.Context(), func(ctx context.Context) error {
		current, err := h.k.DB().RemoteCache().Source(ctx, selected.Key)
		if err != nil {
			return fmt.Errorf("bower registration source: %w", err)
		}
		if current != identity {
			return source.ErrSuperseded
		}
		return h.k.DB().
			BowerRegistrations().
			Create(ctx, model.BowerRegistration{RepoKey: selected.Key, Name: name, URL: raw})
	})
	if errors.Is(err, kernel.ErrConflict) {
		h.k.Error(w, r, apierror.New(http.StatusForbidden, "Bower package is already registered"))
		return
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	jsonAnswer(w, r, h.entry(r, name))
}

func (h handler) registrationTarget(r *http.Request, name, raw string) (*http.Request, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote {
		if _, err := gitPointerURL(c, name, raw); err != nil {
			return nil, source.ErrInvalidRequest
		}
		return r, nil
	}
	members, err := h.virtualMembers(r)
	if err != nil {
		return nil, err
	}
	for _, mr := range members {
		member := pkgroute.RequestFrom(mr).Repo
		if member.Class != repo.Remote {
			continue
		}
		if _, err := gitPointerURL(member, name, raw); err == nil {
			return mr, nil
		}
	}
	return nil, source.ErrInvalidRequest
}

func (h handler) registrationNames(r *http.Request, query string) ([]string, error) {
	c := pkgroute.RequestFrom(r).Repo
	var out []string
	for page := (kernel.Page{Limit: kernel.MaxLimit}); ; {
		rows, next, err := h.k.DB().BowerRegistrations().List(r.Context(), c.Key, page)
		if err != nil {
			return nil, fmt.Errorf("bower registrations: %w", err)
		}
		for _, v := range rows {
			if !strings.Contains(v.Name, query) || !h.k.CanFolder(r, v.Name, api.ActionRead) {
				continue
			}
			if _, err := gitPointerURL(c, v.Name, v.URL); err == nil {
				out = append(out, v.Name)
			}
		}
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

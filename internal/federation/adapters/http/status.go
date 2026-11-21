package federationhttp

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

// RegisterExecutionRoutes claims the authored greedy path routes before the
// router installs its handwritten-operation fallbacks.
func (s ReplicationAPI) RegisterExecutionRoutes(r chi.Router) {
	for _, pattern := range httpkit.ChiPatterns("/artifactory/api/replication/execute/{repoPath}") {
		r.With(middleware.BodyLimit(middleware.ManagementBodyLimit)).Post(pattern, s.execute)
	}
	for _, pattern := range httpkit.ChiPatterns("/artifactory/api/replication/{repoPath}") {
		r.Get(pattern, s.getStatus)
	}
}

func replicationPath(escaped string) (string, string, error) {
	segments := strings.Split(strings.TrimSuffix(escaped, "/"), "/")
	for i, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil || value == "" || strings.ContainsAny(value, "/\\") {
			return "", "", httpkit.BadRequestf("invalid replication path")
		}
		segments[i] = value
	}
	if len(segments) == 0 || segments[0] == "" {
		return "", "", httpkit.BadRequestf("a repository key is required")
	}
	root := strings.Join(segments[1:], "/")
	if root != "" {
		if _, _, _, err := kernel.SplitItemPath(root); err != nil {
			return "", "", httpkit.BadRequestf("invalid replication path")
		}
	}
	return segments[0], root, nil
}

func (s ReplicationAPI) getStatus(w http.ResponseWriter, r *http.Request) {
	if s.Status == nil {
		httpkit.WriteErr(w, r, apierror.ErrNotImplemented)
		return
	}
	key, root, err := replicationPath(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/api/replication/"))
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	view, err := s.Status.Read(r.Context(), key, root)
	if err != nil {
		httpkit.WriteErr(w, r, replicationError(r.Context(), err))
		return
	}
	httpkit.WriteJSON(w, http.StatusOK, statusWire(view))
}

func statusWire(view replication.StatusView) artgen.ReplicationStatus {
	out := artgen.ReplicationStatus{}
	if view.Class == repo.Remote {
		state := replication.TargetStatus{Status: model.ReplicationNeverRun}
		if len(view.Targets) == 1 {
			state = view.Targets[0]
		}
		out.Status = httpkit.PtrTo(artgen.ReplicationStatusStatus(state.Status))
		if !state.LastCompleted.IsZero() {
			out.LastCompleted = &state.LastCompleted
		}
		return out
	}
	targets := make([]artgen.ReplicationTarget, 0, len(view.Targets))
	for _, state := range view.Targets {
		target := artgen.ReplicationTarget{Url: &state.URL, Status: httpkit.PtrTo(artgen.ReplicationTargetStatus(state.Status))}
		if !state.LastCompleted.IsZero() {
			target.LastCompleted = &state.LastCompleted
		}
		if address, err := url.Parse(state.URL); err == nil {
			parts := strings.Split(strings.Trim(address.Path, "/"), "/")
			if key := parts[len(parts)-1]; key != "" {
				target.RepoKey = &key
			}
		}
		targets = append(targets, target)
	}
	out.Targets = &targets
	return out
}

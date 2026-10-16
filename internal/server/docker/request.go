package docker

import (
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Request is a registry request with its operation parsed and, for an
// operation on an image, its repository resolved and its name validated.
type Request struct {
	Kind Kind
	// RepoKey is the repository the request addressed: the subdomain, or
	// the key in /artifactory/api/docker/<key>/v2. It is empty for /v2/,
	// /v2/_catalog and /v2/token on the repository path, which address no
	// repository, and may be "null", which the token realm uses for that
	// (research 17). Only an operation on an image resolves it into Repo.
	RepoKey string
	// Repo is the resolved repository, set when Kind is an image operation.
	Repo repo.Config
	// Name is the image name inside the repository.
	Name string
	// Reference is the digest, tag or upload session the path ends in.
	Reference string
	// base is the path up to the image name in the form the request used,
	// so that Path answers in the same form.
	base string
	// resolved is set once Repo holds the repository RepoKey names.
	resolved bool
}

// Path returns the absolute path of an operation on the request's image in
// the addressing form the client used, for Location headers: the client
// may only know the registry by that form.
// Path("blobs", "uploads", id) is .../<name>/blobs/uploads/<id>.
func (q Request) Path(segments ...string) string {
	return q.base + "/" + q.Name + "/" + strings.Join(segments, "/")
}

// Require answers nil when the caller holds every action in want on path,
// a path inside the repository. Otherwise it answers what the registry
// tells a client: UNAUTHORIZED to an anonymous caller, so that it sends
// credentials, and DENIED to anyone else. A request without a principal
// reached the handler only on a server built without authentication,
// which only tests build, and is allowed.
func (q Request) Require(r *http.Request, path string, want types.Actions) error {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Access() == nil {
		return nil
	}
	if p.Access().Allows(types.ResourceArtifact, authz.Repo{
		Key: q.Repo.Key, Class: string(q.Repo.Class),
	}, path, want) {
		return nil
	}
	if p.Anonymous {
		return Errorf(http.StatusUnauthorized, CodeUnauthorized, "authentication required")
	}
	return Errorf(http.StatusForbidden, CodeDenied,
		"requested access to %s in %s is denied", q.Name, q.Repo.Key)
}

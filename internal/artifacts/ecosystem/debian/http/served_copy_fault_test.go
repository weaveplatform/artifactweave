package debianhttp

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// errStoreRead is a blob store's failed read of a copy it opened.
var errStoreRead = errors.New("debian test: the store failed to read the copy")

// failedRead is a served copy whose reads fail as the store's do.
type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, errStoreRead }
func (failedRead) Close() error             { return nil }

// TestAccDebianRemoteIndexAnswersAFailedReadOfTheServedCopyAsAServerFault: a
// remote repository's Packages and Sources indexes, and a virtual
// repository's Sources index read from that remote member, read the copy
// the source service serves them, the server's cached or spooled file. A
// failed read of it is the server's storage fault, 500 "internal error",
// never the upstream's 502, as the npm and Hex routes now answer one
// (#1488). A virtual's Packages index is merged from those its members'
// Release files list, and none is served here.
func TestAccDebianRemoteIndexAnswersAFailedReadOfTheServedCopyAsAServerFault(t *testing.T) {
	failing := func() (source.Result, error) {
		return source.Result{Stream: &source.Stream{
			Body:           failedRead{},
			Status:         http.StatusOK,
			Representation: source.Representation{Size: 10},
		}}, nil
	}
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debPackages: failing, debSources: failing,
	})))
	w.debRepos(
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-remote"}},
	)
	for _, c := range []struct{ key, path string }{
		{"deb-remote", debPackages}, {"deb-remote", debSources}, {"deb-virtual", debSources},
	} {
		wantError(
			t,
			w.get(c.key, c.path, "admin"),
			http.StatusInternalServerError,
			"internal error",
		)
	}
}

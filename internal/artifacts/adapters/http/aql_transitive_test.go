package artifactshttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func transitiveRow(name string) peerAQLRow {
	return peerAQLRow{
		Repo:       "source",
		Path:       ".",
		Name:       name,
		Type:       "file",
		Size:       7,
		ActualSHA1: sha1Hex("newdata"),
		ActualMD5:  md5Hex("newdata"),
		SHA256:     sha256Hex("newdata"),
	}
}

func transitiveJSON(t *testing.T, rows []peerAQLRow) []byte {
	t.Helper()
	if rows == nil {
		rows = []peerAQLRow{}
	}
	b, err := json.Marshal(
		map[string]any{
			"results": rows,
			"range":   map[string]int{"start_pos": 0, "end_pos": len(rows), "total": len(rows)},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAccTransitiveVirtualTraversalBounds(t *testing.T) {
	for _, depth := range []int{9, 260} {
		configs := map[string]repo.Config{"remote": {Key: "remote", Class: repo.Remote}}
		if depth == 9 {
			next := "remote"
			for i := depth; i >= 0; i-- {
				key := fmt.Sprintf("v%d", i)
				configs[key] = repo.Config{
					Key:          key,
					Class:        repo.Virtual,
					Repositories: []string{next},
				}
				next = key
			}
		} else {
			members := make([]string, depth)
			for i := range members {
				members[i] = "remote"
			}
			configs["v0"] = repo.Config{Key: "v0", Class: repo.Virtual, Repositories: members}
		}
		if _, err := transitiveMembers(configs, "v0"); err == nil {
			t.Fatal("unbounded virtual traversal", depth)
		}
	}
}

// Without an authenticator a transitive search cannot recheck its caller's
// credential, so the caller is refused, with no principal and no decider.
func TestUnitTransitivePrincipalWithoutAuthenticator(t *testing.T) {
	s := newArtifactoryAPI(withAccess(t, Config{Store: inmem.New()}))
	p, d, err := s.transitivePrincipal(t.Context(), kernel.Principal{Name: "admin", Password: true})
	if !errors.Is(err, source.ErrDenied) || d != nil || !reflect.DeepEqual(p, kernel.Principal{}) {
		t.Fatalf("transitivePrincipal = %+v, %v, %v; want source.ErrDenied alone", p, d, err)
	}
}

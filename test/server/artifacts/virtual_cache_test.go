package artifacts_test

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// mergeProtocol is a test-only protocol whose /merged/* route merges the
// document at the path from every member through Collect, one
// "member=content" line each, and keeps it with Merged, as a family's
// virtual.go would. builds counts the merges.
func mergeProtocol(mu *sync.Mutex, builds *int) pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         "vcache",
		Types:        []repo.PackageType{repo.Generic},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources: map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Virtual: pkgroute.Virtual{},
		},
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			read := func(mr *http.Request, p string) (string, error) {
				result, err := k.ReadMetadata(mr, pkgroute.Resource(mr, p, p))
				if err != nil {
					return "", err //nolint:wrapcheck // the kit's own answer
				}
				body, _, err := k.Blobs().Open(mr.Context(), result.Item.Actual.SHA256)
				if err != nil {
					return "", err //nolint:wrapcheck // a test read
				}
				defer func() { _ = body.Close() }()
				b, err := io.ReadAll(body)
				return pkgroute.RequestFrom(mr).Repo.Key + "=" + string(b), err
			}
			r.Get("/merged/*", func(w http.ResponseWriter, r *http.Request) {
				p := strings.TrimPrefix(pkgroute.RequestFrom(r).Rest, "merged/")
				e, err := k.Merged(r, p, p, func(r *http.Request) (virtual.Entry, error) {
					mu.Lock()
					*builds++
					mu.Unlock()
					parts, err := pkgroute.Collect(k, r, p, func(mr *http.Request) (string, error) {
						return read(mr, p)
					})
					lines := make([]string, len(parts))
					for i, part := range parts {
						lines[i] = part.Doc
					}
					return virtual.Entry{Body: []byte(strings.Join(lines, "\n"))}, err
				})
				if err != nil {
					k.Error(w, r, pkgroute.SourceError(err, false))
					return
				}
				_, _ = w.Write(e.Body)
			})
		},
	}
}

// Through the whole server, a virtual repository's merged document is
// kept for its virtualRetrievalCachePeriodSecs, 7200 by default (decision
// 0008, R8): a member deploy leaves it until it expires, as 7.x does for
// every type but Helm; a caller who may read fewer members gets an entry
// of its own; and MemberWritten, which Helm's writes will call, drops the
// entries of the virtual repository holding the member and of the one
// that nests it.
func TestAccVirtualMetadataCache(t *testing.T) {
	var (
		mu     sync.Mutex
		builds int
		now    = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	)
	cache := virtual.NewCache(virtual.CacheOptions{Now: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}})
	w := newItemWorld(t, false, func(c *Config) {
		c.Protocols = append(c.Protocols, mergeProtocol(&mu, &builds))
		c.VirtualCache = cache
	})
	for key, body := range map[string]string{
		"gen-b": `{"rclass":"local"}`,
		"inner": `{"rclass":"virtual","repositories":["libs-local"]}`,
		"outer": `{"rclass":"virtual","repositories":["inner","gen-b"]}`,
	} {
		wantStatus(t, w.do(http.MethodPut, "/artifactory/api/repositories/"+key, "admin", body,
			"Content-Type", "application/json"), http.StatusOK)
	}
	w.put("/artifactory/libs-local/idx/versions", "1.0")
	w.put("/artifactory/gen-b/idx/versions", "2.0")
	get := func(key, user, want string, wantBuilds int) {
		t.Helper()
		rec := w.do(http.MethodGet, "/artifactory/api/vcache/"+key+"/merged/idx/versions", user, "")
		mu.Lock()
		got := builds
		mu.Unlock()
		if rec.Code != http.StatusOK || rec.Body.String() != want || got != wantBuilds {
			t.Fatalf("GET %s as %s: %d %q after %d merges, want %q after %d",
				key, user, rec.Code, rec.Body, got, want, wantBuilds)
		}
	}
	get("outer", "admin", "libs-local=1.0\ngen-b=2.0", 1)
	w.put("/artifactory/libs-local/idx/versions", "1.1")
	get("outer", "admin", "libs-local=1.0\ngen-b=2.0", 1)
	get("outer", "deployer", "libs-local=1.1", 2)
	get("outer", "deployer", "libs-local=1.1", 2)
	get("inner", "admin", "libs-local=1.1", 3)

	if err := cache.MemberWritten(t.Context(), w.db.Repos(), "libs-local", "idx/versions", false); err != nil {
		t.Fatal(err)
	}
	get("inner", "admin", "libs-local=1.1", 4)
	get("outer", "admin", "libs-local=1.1\ngen-b=2.0", 5)
	get("outer", "deployer", "libs-local=1.1", 6)

	w.put("/artifactory/gen-b/idx/versions", "2.1")
	get("outer", "admin", "libs-local=1.1\ngen-b=2.0", 6)
	mu.Lock()
	now = now.Add(2*time.Hour + time.Second)
	mu.Unlock()
	get("outer", "admin", "libs-local=1.1\ngen-b=2.1", 7)
}

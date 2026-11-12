package debian_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
)

// TestAccDebianRemoteIndexPassesMatrixParametersUpstream (#1371): a remote
// Debian repository's Packages and Sources index, and a virtual
// repository's Sources index over it, asked for with matrix parameters,
// ask the upstream with them after the path, as 7.x appends them to the
// remote request URL ("Appending matrix params to remote request URL" in
// the ?trace of RTFACT-26349; HttpRepo.downloadResource, 7.161.26), and
// serve its answer. Before, they answered 404 "Could not find resource"
// without asking the upstream. The index is cached at its bare path, so a
// GET without the parameters, or with a key the copy may lack, is served
// from the cache, and a mandatory key the copy does not carry answers 404
// without asking the upstream, as 7.x answers a cached copy whose
// properties do not match (RemoteRepoBase.internalGetInfo). A user without
// read permission is refused 403 and an anonymous caller 401 before the
// upstream is asked (DV-057).
func TestAccDebianRemoteIndexPassesMatrixParametersUpstream(t *testing.T) {
	files, paths := queryArchive(t)
	for _, p := range paths {
		if !debian.PackagesIndexPath(p) && !debian.SourcesIndexPath(p) {
			continue // the item route serves these as any remote file
		}
		// The upstream is an Artifactory that resolves the parameters, so
		// it serves the file at the URL that carries them.
		files[p+";x=1"] = files[p]
	}
	for _, p := range paths {
		if !debian.PackagesIndexPath(p) && !debian.SourcesIndexPath(p) {
			continue
		}
		for _, key := range []string{"remote", "deb-virtual"} {
			if key == "deb-virtual" && !debian.SourcesIndexPath(p) {
				continue // a virtual composes its Packages index (#1330)
			}
			t.Run(key+"/"+p, func(t *testing.T) {
				w, up := queryRemote(t, files)
				target := "/artifactory/" + key + "/" + p
				for _, tc := range []struct {
					caller, suffix string
					status         int
					asked          []string
				}{
					{"denied", ";x=1", http.StatusForbidden, nil},
					{"", ";x=1", http.StatusUnauthorized, nil},
					{"reader", ";x=1", http.StatusOK, []string{"/" + p + ";x=1"}},
					{"admin", "", http.StatusOK, nil},
					{"admin", ";x=1", http.StatusOK, nil},
					{"reader", ";x+=1", http.StatusNotFound, nil},
				} {
					r := w.Request(http.MethodGet, target+tc.suffix, tc.caller, "")
					switch tc.status {
					case http.StatusForbidden:
						queryRefusal(t, r, tc.status, "Forbidden")
					case http.StatusUnauthorized:
						queryRefusal(t, r, tc.status, "Authentication is required")
					case http.StatusNotFound:
						queryRefusal(t, r, tc.status, "Could not find resource")
					default:
						if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
							t.Errorf("%s%s as %q answered %d %q, want 200 and the index",
								key, tc.suffix, tc.caller, r.Code, r.Body)
						}
					}
					if got := up.take(); !slices.Equal(got, tc.asked) {
						t.Errorf("%s%s as %q asked the upstream %q, want %q",
							key, tc.suffix, tc.caller, got, tc.asked)
					}
				}
			})
		}
	}
}

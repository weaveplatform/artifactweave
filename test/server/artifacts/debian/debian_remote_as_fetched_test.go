package debian_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// asFetchedArchive is an upstream whose distribution "mixed" holds indexes
// apt reads that do not decode, and a Release listing a plain Packages
// index for three architectures: amd64's does not decode, arm64's is
// whole, and i386's does not have the digest the Release gives it. It
// returns the files and the paths of the indexes that do not decode.
func asFetchedArchive(t *testing.T) (map[string][]byte, []string) {
	t.Helper()
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	index := []byte(stanza("pool/main/a/alpha/alpha_1.0_amd64.deb", deb))
	corrupt := map[string][]byte{
		"dists/mixed/main/binary-amd64/Packages": []byte("Package alpha\n"),
		"dists/mixed/main/binary-amd64/Packages.gz": []byte(
			"invalid Packages index",
		),
		"dists/mixed/main/binary-amd64/Packages.xz": []byte("\xfd7zXZ\x00garbage"),
		"dists/mixed/main/source/Sources":           []byte("not a sources index"),
		"dists/mixed/main/debian-installer/binary-amd64/Packages.gz": []byte(
			"invalid installer index",
		),
	}
	release, err := debian.Release("mixed", []string{"main"}, []string{"amd64", "arm64", "i386"},
		[]debian.ReleaseEntry{
			{
				Path: "main/binary-amd64/Packages",
				Body: corrupt["dists/mixed/main/binary-amd64/Packages"],
			},
			{Path: "main/binary-arm64/Packages", Body: index},
			{Path: "main/binary-i386/Packages", Body: []byte("an index the upstream replaced\n")},
		}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"dists/mixed/Release":                    release,
		"dists/mixed/main/binary-arm64/Packages": index,
		"dists/mixed/main/binary-i386/Packages":  index,
	}
	paths := make([]string, 0, len(corrupt))
	for p, body := range corrupt {
		files[p] = body
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return files, paths
}

// TestAccDebianRemoteCachesAnIndexItCannotDecodeAsFetched (#1356): a
// remote Debian repository serves and caches its upstream's Packages,
// Sources or installer index as fetched, plain, gzipped or xz, whether or
// not it decodes, where it answered 502 "Bad Gateway" and cached nothing
// before. RTFACT-27303 (open, 7.37.14) asks 7.x to check an upstream
// Packages index against its Release before caching it, so 7.x caches it
// unchecked today; a Sources and an installer index are treated alike [I].
// A reader is served the upstream's bytes, the next caller and the -cache
// alias are served the cached copy without asking the upstream again, and a
// user without read permission is refused 403 and an anonymous caller 401
// before the upstream is asked. The -cache alias serves an installer
// index's cached copy too, where it answered 404 (#1382).
func TestAccDebianRemoteCachesAnIndexItCannotDecodeAsFetched(t *testing.T) {
	files, paths := asFetchedArchive(t)
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			w, up := queryRemote(t, files)
			queryRefusal(t, w.Request(http.MethodGet, "/artifactory/remote/"+p, "denied", ""),
				http.StatusForbidden, "Forbidden")
			queryRefusal(t, w.Request(http.MethodGet, "/artifactory/remote/"+p, "", ""),
				http.StatusUnauthorized, "Authentication is required")
			for _, as := range []struct{ caller, key string }{
				{"reader", "remote"}, {"admin", "remote"}, {"admin", "remote-cache"},
			} {
				r := w.Request(http.MethodGet, "/artifactory/"+as.key+"/"+p, as.caller, "")
				if r.Code != http.StatusOK || r.Body.String() != string(files[p]) {
					t.Errorf("%s %s answered %d %q, want 200 and the upstream's bytes",
						as.caller, as.key, r.Code, r.Body)
				}
			}
			if got := up.take(); !slices.Equal(got, []string{"/" + p}) {
				t.Errorf("the upstream was asked %q, want %q once", got, "/"+p)
			}
		})
	}
}

// TestAccDebianVirtualLeavesOutAMemberIndexItCannotUse (#1356): a virtual
// repository over a remote leaves out of the metadata it composes a member
// index that does not decode or does not have the digest the member's
// Release gives it, as a member without the index is [I], where it answered
// 502 "Bad Gateway" for that distribution's Release and indexes before. The
// remote caches such an index as fetched (RTFACT-27303), so the virtual
// meets it; 7.x would have to check it to merge it. The distribution's
// other indexes are still merged, an index left out is not found, and the
// member's Sources index, which the virtual relays rather than composes, is
// served as fetched.
func TestAccDebianVirtualLeavesOutAMemberIndexItCannotUse(t *testing.T) {
	files, _ := asFetchedArchive(t)
	w, _ := queryRemote(t, files)
	const virtual = "/artifactory/deb-virtual/dists/mixed/"
	r := w.Request(http.MethodGet, virtual+"Release", "admin", "")
	if r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), "main/binary-arm64/Packages") ||
		strings.Contains(r.Body.String(), "binary-amd64") ||
		strings.Contains(r.Body.String(), "binary-i386") {
		t.Errorf("virtual Release answered %d %q, want 200 listing only the arm64 index",
			r.Code, r.Body)
	}
	r = w.Request(http.MethodGet, virtual+"main/binary-arm64/Packages", "admin", "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Package: alpha") {
		t.Errorf("virtual arm64 Packages answered %d %q, want 200 with alpha", r.Code, r.Body)
	}
	for _, arch := range []string{"amd64", "i386"} {
		queryRefusal(t, w.Request(http.MethodGet, virtual+"main/binary-"+arch+"/Packages",
			"admin", ""), http.StatusNotFound, "Could not find resource")
	}
	r = w.Request(http.MethodGet, virtual+"main/source/Sources", "admin", "")
	if r.Code != http.StatusOK ||
		r.Body.String() != string(files["dists/mixed/main/source/Sources"]) {
		t.Errorf("virtual Sources answered %d %q, want 200 and the member's bytes", r.Code, r.Body)
	}
}

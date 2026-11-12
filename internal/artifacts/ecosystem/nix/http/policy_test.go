package nixhttp_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func wantBody(t *testing.T, w *httptest.ResponseRecorder, code int, text string) {
	t.Helper()
	if w.Code != code || !strings.Contains(w.Body.String(), text) {
		t.Fatalf("status %d want %d, body %q want %q", w.Code, code, w.Body, text)
	}
}

func blackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func rejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err = f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// native is the NAR URL a narinfo served from key names.
func (f *fixture) native(key string) string {
	return "/artifactory/api/nix/" + key + "/nar/" + infoPath[:32] + "/" +
		strings.TrimPrefix(f.info.URL, "nar/")
}

func (f *fixture) wantArchive(path string) {
	f.t.Helper()
	w := f.do("GET", path, nil, "")
	status(f.t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), f.data) {
		f.t.Fatal("archive changed")
	}
}

// The download policy is checked at the file the client asked for, after
// Read permission, where the repository stores it: nix-cache-info refuses
// a caller who may not read with 403, not the blackout's 404; a narinfo
// stays served when the patterns reject only its NAR, as 7.x serves a
// stored document whose release's file is refused; and a NAR request is
// checked at the NAR, so its refusal names the NAR and patterns rejecting
// only the narinfo it was found through leave it served. Both are stored
// in the store path's binary-cache folder, as 7.x stores them, so the
// refusals name those paths and patterns written for 7.x, such as
// binary-cache/**/*.nar.xz, match them (#1296).
func TestAccNixHostedPolicyChecksThePathAskedFor(t *testing.T) {
	f := newFixture(t)
	f.publish()
	info, nar := prefix+infoPath, f.native("local")
	on, off := true, false
	excludeNAR, excludeInfo := "binary-cache/**/*.nar.xz", "binary-cache/**/*.narinfo"
	f.patch("local", func(c *repo.Config) { c.BlackedOut = &on })
	status(t, f.do("GET", prefix+"nix-cache-info", nil, "denied"), 403)
	wantBody(t, f.do("GET", prefix+"nix-cache-info", nil, ""), 404,
		blackedOut("local", "nix-cache-info"))
	wantBody(t, f.do("GET", info, nil, ""), 404, blackedOut("local", storedInfo))
	wantBody(t, f.do("GET", nar, nil, ""), 404, blackedOut("local", f.storedNAR()))
	status(t, f.do("GET", nar, nil, "denied"), 403)
	f.patch("local", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excludeNAR })
	status(t, f.do("GET", info, nil, ""), 200)
	wantBody(t, f.do("GET", nar, nil, ""), 404, rejected("local", f.storedNAR()))
	f.patch("local", func(c *repo.Config) { c.ExcludesPattern = &excludeInfo })
	wantBody(t, f.do("GET", info, nil, ""), 404, rejected("local", storedInfo))
	f.wantArchive(nar)
	// Patterns written for the client's own paths match nothing stored.
	clientPaths := "nar/**,*.narinfo"
	f.patch("local", func(c *repo.Config) { c.ExcludesPattern = &clientPaths })
	status(t, f.do("GET", info, nil, ""), 200)
	f.wantArchive(nar)
}

// A virtual repository passes over a member that refuses the path asked
// for, blacked out or with patterns that reject it, where the member's own
// refusal answered; a member whose patterns reject only the NAR keeps its
// narinfo served, and its NAR comes from the next member. The virtual
// repository's own patterns refuse only the path asked for, a NAR with
// "No repository found to serve the request", not "Not Found". Both check
// the paths a hosted repository stores the files at.
func TestAccNixVirtualPassesOverRefusingMembers(t *testing.T) {
	f := newFixture(t)
	f.publish()
	first := f.c
	first.Key = "first"
	v := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix,
		Repositories: []string{"first", "local"},
	})
	for _, c := range []repo.Config{first, v} {
		if err := f.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	marked := f.info
	marked.Deriver = infoPath[:32] + "-first.drv"
	firstBase := "/artifactory/api/nix/first/"
	status(t, f.do("PUT", firstBase+f.info.URL, f.data, ""), 201)
	status(t, f.do("PUT", firstBase+infoPath, marked.Encode(f.info.URL), ""), 201)
	info, nar := "/artifactory/api/nix/virtual/"+infoPath, f.native("virtual")
	served := func(fromFirst bool) {
		t.Helper()
		w := f.do("GET", info, nil, "")
		status(t, w, 200)
		if strings.Contains(w.Body.String(), "Deriver: "+infoPath[:32]+"-first.drv") != fromFirst {
			t.Fatalf("narinfo from the wrong member: %s", w.Body)
		}
		f.wantArchive(nar)
	}
	served(true)
	on, off := true, false
	excludeNAR, excludeInfo := "binary-cache/**/*.nar.xz", "binary-cache/**/*.narinfo"
	f.patch("first", func(c *repo.Config) { c.BlackedOut = &on })
	served(false)
	f.patch("first", func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excludeInfo })
	served(false)
	f.patch("first", func(c *repo.Config) { c.ExcludesPattern = &excludeNAR })
	served(true)
	f.patch("first", func(c *repo.Config) { c.ExcludesPattern = nil })
	f.patch("virtual", func(c *repo.Config) { c.ExcludesPattern = &excludeNAR })
	status(t, f.do("GET", info, nil, ""), 200)
	wantBody(t, f.do("GET", nar, nil, ""), 404,
		"No repository found to serve the request for virtual:"+f.storedNAR())
	f.patch("virtual", func(c *repo.Config) { c.ExcludesPattern = &excludeInfo })
	wantBody(t, f.do("GET", info, nil, ""), 404,
		"No repository found to serve the request for virtual:"+storedInfo)
	f.wantArchive(nar)
}

// A remote checks its policy at the path the client asked for too. Patterns
// rejecting only the NAR leave the narinfo served and refuse the NAR with
// their 404, where both answered 502, which turns the Nix client's whole
// substituter off; the NAR is never fetched. Patterns rejecting only the
// narinfo leave the NAR served, and a blacked-out remote's refusal of a NAR
// names the NAR.
func TestAccNixRemotePolicyChecksThePathAskedFor(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	excludeNAR, excludeInfo := "nar/**", "*.narinfo"
	c.ExcludesPattern = &excludeNAR
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", base+infoPath, nil, "")
	status(t, w, 200)
	nar := base + downloadURL(w.Body.Bytes())
	wantBody(t, f.do("GET", nar, nil, ""), 404, rejected("remote", f.info.URL))
	if up.Count("", "/"+f.info.URL) != 0 {
		t.Fatal("a refused NAR was fetched upstream")
	}
	c.ExcludesPattern = &excludeInfo
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do("GET", base+infoPath, nil, ""), 404, rejected("remote", infoPath))
	f.wantArchive(nar)
	on := true
	c.ExcludesPattern, c.BlackedOut = nil, &on
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantBody(t, f.do("GET", base+infoPath, nil, ""), 404, blackedOut("remote", infoPath))
	wantBody(t, f.do("GET", nar, nil, ""), 404, blackedOut("remote", f.info.URL))
}

// A remote member whose patterns reject only the NAR keeps its narinfo in a
// virtual repository and is passed over for the NAR, which the next member
// serves; both answered 502.
func TestAccNixVirtualPassesOverARemoteMembersRefusedNAR(t *testing.T) {
	f, up, c, _ := remoteFixture(t)
	f.publish()
	excludeNAR := "nar/**"
	c.ExcludesPattern = &excludeNAR
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	v := *repo.Defaults(&repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Nix,
		Repositories: []string{"remote", "local"},
	})
	if err := f.db.Repos().Create(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	marked := f.info
	marked.Deriver = infoPath[:32] + "-upstream.drv"
	if err := marked.Sign("cache-1", f.private); err != nil {
		t.Fatal(err)
	}
	up.Set("/"+infoPath, fakeupstream.Resource{
		Header: http.Header{"Content-Type": {"text/x-nix-narinfo"}},
		Body:   marked.Encode(marked.URL),
	})
	w := f.do("GET", "/artifactory/api/nix/virtual/"+infoPath, nil, "")
	status(t, w, 200)
	if !strings.Contains(w.Body.String(), "Deriver: "+infoPath[:32]+"-upstream.drv") {
		t.Fatalf("narinfo not from the remote member: %s", w.Body)
	}
	f.wantArchive(f.native("virtual"))
	if up.Count("", "/"+f.info.URL) != 0 {
		t.Fatal("a refused NAR was fetched upstream")
	}
}

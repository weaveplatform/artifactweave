package pubhttp_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// patch changes repository key's configuration.
func (f *fixture) patch(key string, change func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), key)
	if err != nil {
		f.t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

// wantPubError fails unless rec is the pub error document with status and
// message, and nothing else.
func wantPubError(
	t *testing.T,
	label string,
	rec *httptest.ResponseRecorder,
	status int,
	message string,
) {
	t.Helper()
	want := `{"error":{"code":"` + map[int]string{
		http.StatusNotFound: "NOT_FOUND", http.StatusForbidden: "FORBIDDEN",
	}[status] + `","message":"` + message + `"}}` + "\n"
	if rec.Code != status || rec.Body.String() != want ||
		rec.Header().Get("Content-Type") != "application/vnd.pub.v2+json" {
		t.Errorf("%s: %d %q (%s), want %d %q", label, rec.Code, rec.Body,
			rec.Header().Get("Content-Type"), status, want)
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

// TestUnitDownloadPolicyRefusesTheVersionsDocumentAtItsStoredPath: 7.x keeps
// a package's versions at .pub/<name>.json, so a blacked-out local or
// federated repository, or a pattern on that path, refuses the versions and
// version routes with the policy's 404 naming it, after read permission; a
// pattern on one archive leaves the release listed and refuses only the
// archive (#1237).
func TestUnitDownloadPolicyRefusesTheVersionsDocumentAtItsStoredPath(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		f := newFixture(t, class)
		data := archive(t, "hello_pub", "1.0.0")
		f.publish(f.c.Key, data)
		f.publish(f.c.Key, archive(t, "hello_pub", "1.1.0"))
		f.grant("nothing", accessapi.ActionRead, map[string][]string{f.c.Key: {"other/**"}})
		versions := base + "/api/packages/hello_pub"
		version := versions + "/versions/1.0.0"
		file := base + "/packages/hello_pub/versions/1.0.0.tar.gz"
		document := ".pub/hello_pub.json"
		on, off := true, false
		f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut = &on })
		for label, path := range map[string]string{"versions": versions, "version": version} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				if label == "version" && method == http.MethodHead {
					continue
				}
				rec := f.do(method, path, "admin", "", nil)
				if method == http.MethodHead {
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s HEAD %s: %d", class, label, rec.Code)
					}
					continue
				}
				wantPubError(t, string(class)+" blacked-out "+label, rec, http.StatusNotFound,
					blackedOut("pub-local", document))
			}
		}
		wantPubError(
			t,
			string(class)+" blacked-out archive",
			f.do(http.MethodGet, file, "admin", "", nil),
			http.StatusNotFound,
			blackedOut("pub-local", "hello_pub/hello_pub-1.0.0.tar.gz"),
		)
		wantPubError(t, string(class)+" blacked-out, unreadable",
			f.do(http.MethodGet, versions, "nothing", "", nil), http.StatusForbidden, "Forbidden")
		excluded := ".pub/**"
		f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
		wantPubError(
			t,
			string(class)+" excluded versions",
			f.do(http.MethodGet, versions, "admin", "", nil),
			http.StatusNotFound,
			rejected("pub-local", document),
		)
		wantPubError(
			t,
			string(class)+" excluded version",
			f.do(http.MethodGet, version, "admin", "", nil),
			http.StatusNotFound,
			rejected("pub-local", document),
		)
		if rec := f.do(http.MethodGet, file, "admin", "", nil); rec.Code != http.StatusOK ||
			!bytes.Equal(rec.Body.Bytes(), data) {
			t.Errorf("%s archive beside an excluded document: %d %s", class, rec.Code, rec.Body)
		}
		excluded = "hello_pub/hello_pub-1.0.0.tar.gz"
		f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = &excluded })
		if _, listed := f.versionsOf(f.c.Key, "hello_pub", "admin"); !slices.Equal(
			listed, []string{"1.1.0", "1.0.0"},
		) {
			t.Errorf("%s versions with an excluded archive: %v", class, listed)
		}
		wantPubError(
			t,
			string(class)+" excluded archive",
			f.do(http.MethodGet, file, "admin", "", nil),
			http.StatusNotFound,
			rejected("pub-local", "hello_pub/hello_pub-1.0.0.tar.gz"),
		)
		excluded = ""
		f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = &excluded })
		if _, listed := f.versionsOf(f.c.Key, "hello_pub", "admin"); len(listed) != 2 {
			t.Errorf("%s versions served again: %v", class, listed)
		}
	}
}

// TestUnitDownloadPolicyThroughAVirtualRepository: a virtual repository's
// versions leave out a blacked-out member and one whose patterns reject the
// document, and list a release whose archive a member's patterns reject;
// its archive download passes over a member that refuses the archive and
// serves the next member's copy; and its own patterns refuse the document
// or the archive with "No repository found to serve the request" (#1237).
func TestUnitDownloadPolicyThroughAVirtualRepository(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for _, key := range []string{"pub-a", "pub-b"} {
		f.createRepo(repo.Config{Key: key, Class: repo.Local, PackageType: repo.Pub})
	}
	fromA := describedArchive(t, "hello_pub", "1.0.0", "from a")
	fromB := describedArchive(t, "hello_pub", "1.0.0", "from b")
	f.publish("pub-a", fromA)
	f.publish("pub-b", fromB)
	f.setMembers("pub-a", "pub-b")
	versions := base + "/api/packages/hello_pub"
	file := base + "/packages/hello_pub/versions/1.0.0.tar.gz"
	archivePath := "hello_pub/hello_pub-1.0.0.tar.gz"
	served := func(label string, want []byte) {
		t.Helper()
		doc, _ := f.versionsOf(f.c.Key, "hello_pub", "admin")
		description := map[string]string{string(fromA): "from a", string(fromB): "from b"}[string(want)]
		if len(doc.Versions) != 1 || doc.Versions[0].Pubspec["description"] != description {
			t.Errorf("%s: versions %+v, want %q", label, doc.Versions, description)
		}
		rec := f.do(http.MethodGet, file, "admin", "", nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("%s: archive %d %s", label, rec.Code, rec.Body)
		}
	}
	served("both members", fromA)
	on, off := true, false
	f.patch("pub-a", func(c *repo.Config) { c.BlackedOut = &on })
	served("first member blacked out", fromB)
	f.patch("pub-a", func(c *repo.Config) { c.BlackedOut = &off })
	excluded := archivePath
	f.patch("pub-a", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	doc, _ := f.versionsOf(f.c.Key, "hello_pub", "admin")
	if len(doc.Versions) != 1 || doc.Versions[0].Pubspec["description"] != "from a" {
		t.Errorf("first member's archive excluded: versions %+v", doc.Versions)
	}
	if rec := f.do(http.MethodGet, file, "admin", "", nil); rec.Code != http.StatusOK ||
		!bytes.Equal(rec.Body.Bytes(), fromB) {
		t.Errorf("first member's archive excluded: archive %d %s", rec.Code, rec.Body)
	}
	f.patch("pub-b", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantPubError(
		t,
		"every member excludes the archive",
		f.do(http.MethodGet, file, "admin", "", nil),
		http.StatusNotFound,
		"version not found",
	)
	excluded = ".pub/**"
	f.patch("pub-a", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	f.patch("pub-b", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	doc, _ = f.versionsOf(f.c.Key, "hello_pub", "admin")
	if len(doc.Versions) != 1 || doc.Versions[0].Pubspec["description"] != "from b" {
		t.Errorf("first member's document excluded: versions %+v", doc.Versions)
	}
	if rec := f.do(http.MethodGet, file, "admin", "", nil); rec.Code != http.StatusOK ||
		!bytes.Equal(rec.Body.Bytes(), fromA) {
		t.Errorf("first member's document excluded: archive %d %s", rec.Code, rec.Body)
	}
	f.patch("pub-a", func(c *repo.Config) { c.ExcludesPattern = new(string) })
	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantPubError(
		t,
		"virtual excludes the document",
		f.do(http.MethodGet, versions, "admin", "", nil),
		http.StatusNotFound,
		"No repository found to serve the request for pub-local:.pub/hello_pub.json",
	)
	if rec := f.do(http.MethodGet, file, "admin", "", nil); rec.Code != http.StatusOK ||
		!bytes.Equal(rec.Body.Bytes(), fromA) {
		t.Errorf("virtual excludes the document: archive %d %s", rec.Code, rec.Body)
	}
	excluded = archivePath
	f.patch(f.c.Key, func(c *repo.Config) { c.ExcludesPattern = &excluded })
	wantPubError(t, "virtual excludes the archive", f.do(http.MethodGet, file, "admin", "", nil),
		http.StatusNotFound, "No repository found to serve the request for pub-local:"+archivePath)
	if _, listed := f.versionsOf(f.c.Key, "hello_pub", "admin"); !slices.Equal(
		listed, []string{"1.0.0"},
	) {
		t.Errorf("virtual excludes the archive: versions %v", listed)
	}
}

// TestUnitDownloadPolicyOfARemoteRepository: a remote's own blackout and
// patterns refuse its versions document at .pub/<name>.json and its
// archive at the archive's path before either is read; a release whose
// archive the remote's patterns reject stays listed; the versions read
// names the document's path for the source boundary's policy and the
// lookup an archive download makes checks only the blackout; and a
// virtual repository leaves a blacked-out remote member out (#1237).
func TestUnitDownloadPolicyOfARemoteRepository(t *testing.T) {
	data := archive(t, "visible_pub", "1.0.0")
	archivePath := "visible_pub/visible_pub-1.0.0.tar.gz"
	refuseArchive := func(context.Context, string) error {
		return format.Rejectf(http.StatusNotFound, "%s", rejected("pub-local", archivePath))
	}
	var asked []source.Resource
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = checkedSource{
			metadataSource: metadataSource{check: refuseArchive, archive: data, archiveSet: true},
			asked:          &asked,
		}
	})
	if _, listed := f.versionsOf(f.c.Key, "visible_pub", "admin"); !slices.Equal(
		listed, []string{"1.0.0"},
	) {
		t.Fatalf("release whose archive the remote refuses: %v", listed)
	}
	if len(asked) != 1 || asked[0].PolicyPath != ".pub/visible_pub.json" || asked[0].BlackoutOnly {
		t.Fatalf("versions read %+v", asked)
	}
	wantPubError(t, "refused archive",
		f.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz", "admin", "", nil),
		http.StatusNotFound, rejected("pub-local", archivePath))
	if last := asked[len(asked)-1]; last.Kind != source.ResourceKindMetadata ||
		last.PolicyPath != ".pub/visible_pub.json" || !last.BlackoutOnly {
		t.Fatalf("archive's lookup %+v", last)
	}
	on := true
	f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut = &on })
	asked = nil
	wantPubError(t, "blacked-out remote versions",
		f.do(http.MethodGet, base+"/api/packages/visible_pub", "admin", "", nil),
		http.StatusNotFound, blackedOut("pub-local", ".pub/visible_pub.json"))
	wantPubError(t, "blacked-out remote version",
		f.do(http.MethodGet, base+"/api/packages/visible_pub/versions/1.0.0", "admin", "", nil),
		http.StatusNotFound, blackedOut("pub-local", ".pub/visible_pub.json"))
	wantPubError(t, "blacked-out remote archive",
		f.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz", "admin", "", nil),
		http.StatusNotFound, blackedOut("pub-local", archivePath))
	if len(asked) != 0 {
		t.Fatalf("a blacked-out remote was read: %+v", asked)
	}
	off, excluded := false, ".pub/**"
	f.patch(f.c.Key, func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &off, &excluded })
	wantPubError(t, "remote excludes the document",
		f.do(http.MethodGet, base+"/api/packages/visible_pub", "admin", "", nil),
		http.StatusNotFound, rejected("pub-local", ".pub/visible_pub.json"))

	allowed := func(context.Context, string) error { return nil }
	v := newFixtureWith(t, repo.Virtual, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = keyedSource{
			"pub-remote": {check: allowed, archive: data, archiveSet: true},
			"pub-down":   {readError: source.ErrUnavailable},
		}
	})
	v.createRepo(repo.Config{
		Key: "pub-remote", Class: repo.Remote, PackageType: repo.Pub,
		URL: "https://example.com/pub/", BlackedOut: &on,
	})
	v.createRepo(repo.Config{
		Key: "pub-down", Class: repo.Remote, PackageType: repo.Pub, URL: "https://example.com/pub/",
	})
	v.createRepo(repo.Config{Key: "pub-b", Class: repo.Local, PackageType: repo.Pub})
	fromB := describedArchive(t, "visible_pub", "1.0.0", "from b")
	v.publish("pub-b", fromB)
	v.setMembers("pub-remote", "pub-b")
	doc, _ := v.versionsOf(v.c.Key, "visible_pub", "admin")
	if len(doc.Versions) != 1 || doc.Versions[0].Pubspec["description"] != "from b" {
		t.Errorf("blacked-out remote member: versions %+v", doc.Versions)
	}
	rec := v.do(
		http.MethodGet,
		base+"/packages/visible_pub/versions/1.0.0.tar.gz",
		"admin",
		"",
		nil,
	)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fromB) {
		t.Errorf("blacked-out remote member: archive %d %s", rec.Code, rec.Body)
	}
	v.patch("pub-remote", func(c *repo.Config) { c.BlackedOut = &off })
	rec = v.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz", "admin", "", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Errorf("remote member back: archive %d %s", rec.Code, rec.Body)
	}
	excluded = archivePath
	v.patch("pub-remote", func(c *repo.Config) { c.ExcludesPattern = &excluded })
	rec = v.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz", "admin", "", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fromB) {
		t.Errorf("remote member excludes the archive: archive %d %s", rec.Code, rec.Body)
	}
	// Every member's versions are read before an archive is chosen, as the
	// list reads them, so a member whose source fails fails the download.
	v.setMembers("pub-b", "pub-down")
	rec = v.do(http.MethodGet, base+"/packages/visible_pub/versions/1.0.0.tar.gz", "admin", "", nil)
	if status, message := pubError(t, rec); status != http.StatusBadGateway ||
		message != "Bad Gateway" {
		t.Errorf("failing member: archive %d %q", status, message)
	}
}

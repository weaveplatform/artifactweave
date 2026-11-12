package puppethttp_test

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// recordOf returns the upstream's record of acme-widget at version whose
// checksum and size are data's, and tags's tags.
func recordOf(t *testing.T, version string, data []byte, tags ...string) string {
	t.Helper()
	return releaseBody(t, version, map[string]any{
		"file_sha256": sum(data), "file_size": len(data), "tags": append([]string{}, tags...),
	})
}

// archiveSource returns a remote's source whose upstream lists acme-widget
// 1.0.0 in its release pages, serves body as the record of every release
// it is asked for, recording each read in reads, and serves archive as
// every archive.
func archiveSource(t *testing.T, body string, archive []byte, reads *[]source.Resource) metadataSource {
	t.Helper()
	s := metadataSource{
		body:       remotePageBody(t, upstreamRelease(t, "1.0.0", nil)),
		archive:    archive,
		archiveSet: true,
		check:      func(_ context.Context, _ string) error { return nil },
	}
	served(body, reads)(&s)
	return s
}

// A remote serves the archive of a release its upstream's release pages
// leave out from the address the upstream's record of that release names,
// once the archive matches the record's checksum, size, identity and
// metadata, as v3/releases/<slug> answers that record (#1344). r10k reads
// the record and then fetches its file_uri, so without the archive it could
// not install the release (#1369) [I]. The record is read with the
// blackout alone, as the pages are read to find a listed release's archive
// (R4), after Read and the policy at the archive's own path.
func TestUnitRemoteArchiveOfAReleaseTheListLeavesOut(t *testing.T) {
	data := linkedArchive(t, "2.0.0")
	var reads []source.Resource
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = archiveSource(t, recordOf(t, "2.0.0", data), data, &reads)
	})
	const archive = base + "/v3/files/acme-widget-2.0.0.tar.gz"
	r := f.do("GET", archive, "admin")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), data) {
		t.Fatalf("archive: %d %s", r.Code, r.Body.String())
	}
	if r := f.do("HEAD", archive, "admin"); r.Code != 200 || r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	if len(reads) != 2 {
		t.Fatalf("record reads: %d", len(reads))
	}
	got := reads[0]
	fields := []any{got.Path, got.Variant, got.PermissionPath, got.PolicyPath, got.BlackoutOnly}
	want := []any{
		"v3/releases/acme-widget-2.0.0", "puppet-forge-v3-release-v1", "acme-widget",
		".puppet/acme-widget/releases.json", true,
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("record read: %+v", got)
	}
	// The record itself is still read with the listing's policy.
	reads = nil
	if r := f.do("GET", base+"/v3/releases/acme-widget-2.0.0", "admin"); r.Code != 200 ||
		len(reads) != 1 || reads[0].BlackoutOnly {
		t.Fatalf("release: %d %+v", r.Code, reads)
	}
	// A caller without Read is refused before the record is read.
	reads = nil
	wantError(t, f.do("GET", archive, "denied"), 403, "Forbidden")
	if len(reads) != 0 {
		t.Fatalf("a denied caller read the record: %+v", reads)
	}
}

// An archive that does not match the record it was read through is refused
// with 502, as a listed release's archive that does not match its page is;
// a record the upstream does not hold, or that the source denies the
// caller, still answers 404 "Puppet release not found", and a record that
// is not one valid release answers 502.
func TestUnitRemoteArchiveFromTheRecordFailures(t *testing.T) {
	data := linkedArchive(t, "2.0.0")
	other := linkedArchive(t, "2.0.1")
	for _, tc := range []struct {
		name    string
		tune    func(*metadataSource)
		status  int
		message string
	}{
		{"another archive", func(s *metadataSource) { s.archive = other }, 502, "Bad Gateway"},
		{"truncated archive", func(s *metadataSource) { s.archive = data[:10] }, 502, "Bad Gateway"},
		{"another release's archive", func(s *metadataSource) {
			served(recordOf(t, "2.0.0", other), nil)(s)
			s.archive = other
		}, 502, "Bad Gateway"},
		{"no record", func(s *metadataSource) { s.release.at = nil }, 404, "Puppet release not found"},
		{"denied", func(s *metadataSource) {
			s.release.check = func(_ context.Context, path string) error {
				if path == "acme-widget/acme-widget-2.0.0.tar.gz" {
					return source.ErrDenied
				}
				return nil
			}
		}, 404, "Puppet release not found"},
		{"invalid record", func(s *metadataSource) {
			served(releaseBody(t, "2.0.0", map[string]any{"file_sha256": "x"}), nil)(s)
		}, 502, "invalid upstream Puppet metadata"},
		{"refused", func(s *metadataSource) {
			s.release.readError = format.Rejectf(404, "refused")
		}, 404, "refused"},
		{"upstream 503", func(s *metadataSource) {
			s.release.readError = &source.HTTPError{Status: 503}
		}, 503, "Service Unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := archiveSource(t, recordOf(t, "2.0.0", data), data, nil)
			tc.tune(&src)
			f := newFixtureWith(t, repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
			wantError(t, f.do("GET", base+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"),
				tc.status, tc.message)
		})
	}
}

// A remote whose patterns reject the archive refuses it with the policy's
// 404 before the record is read, as it refuses a listed release's archive.
func TestUnitRemoteArchiveFromTheRecordRefusedFirst(t *testing.T) {
	data := linkedArchive(t, "2.0.0")
	var reads []source.Resource
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) {
		p.Sources[repo.Remote] = archiveSource(t, recordOf(t, "2.0.0", data), data, &reads)
	})
	f.c.ExcludesPattern = new("**/*-2.0.0.tar.gz")
	f.save(f.c)
	wantPolicy(t, f.do("GET", base+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"),
		"The repository 'puppet-local' rejected the artifact "+
			"'puppet-local:acme-widget/acme-widget-2.0.0.tar.gz' due to its include/exclude "+
			"pattern settings.")
	if len(reads) != 0 {
		t.Fatalf("a refused archive read the record: %+v", reads)
	}
}

// remoteMembers returns a fixture whose virtual repository, "virtual", has
// the members keys, in order: "puppet-local" is a remote over
// https://example.com/puppet/, "other" a remote over
// https://other.example/puppet/, and "hosted" a local repository. Each
// remote's upstream lists acme-widget 1.0.0 in its pages and answers the
// record of a release with records[its host], or 404 when that is empty.
func remoteMembers(
	t *testing.T,
	records map[string]string,
	archive []byte,
	keys ...string,
) *fixture {
	t.Helper()
	return tunedMembers(t, nil, records, archive, keys...)
}

// tunedMembers is remoteMembers with tune's changes to the remotes' source.
func tunedMembers(
	t *testing.T,
	tune func(*metadataSource),
	records map[string]string,
	archive []byte,
	keys ...string,
) *fixture {
	t.Helper()
	src := archiveSource(t, "", archive, nil)
	src.pagesAtURL = true
	src.release.at = func(r source.Resource) (string, bool) {
		for host, body := range records {
			if strings.HasPrefix(r.ExternalURL, "https://"+host+"/") {
				return body, body == ""
			}
		}
		return "", true
	}
	if tune != nil {
		tune(&src)
	}
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
	f.create(repo.Config{
		Key: "other", Class: repo.Remote, PackageType: repo.Puppet, URL: "https://other.example/puppet/",
	})
	f.create(repo.Config{Key: "hosted", Class: repo.Local, PackageType: repo.Puppet})
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: keys,
	})
	return f
}

// A virtual repository answers v3/releases/<slug> and the archive of a
// release every member's release pages leave out from the first remote
// member, in member order, whose upstream's record holds it, as that
// remote answers them (#1369) [I]. A hosted member's release of the same
// version is listed, and so it wins.
func TestUnitVirtualReleaseOnlyAnUpstreamRecordHolds(t *testing.T) {
	const v = "/artifactory/api/puppet/virtual"
	data := linkedArchive(t, "2.0.0")
	first, second := recordOf(t, "2.0.0", data, "first"), recordOf(t, "2.0.0", data, "second")
	record := func(tags string) string {
		return strings.Replace(remoteRecordJSON("2.0.0", linkedMetadata("2.0.0"), tags),
			`"file_size":100,`, `"file_size":`+strconv.Itoa(len(data))+`,`, 1)
	}
	withSum := func(s string) string {
		return strings.Replace(s, strings.Repeat("a", 64), sum(data), 1) + "\n"
	}
	f := remoteMembers(t, map[string]string{"example.com": first, "other.example": second}, data,
		"hosted", "puppet-local", "other")
	wantBody(t, f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "admin"),
		withSum(record(`["first"]`)))
	if r := f.do("GET", v+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"); r.Code != 200 ||
		!bytes.Equal(r.Body.Bytes(), data) {
		t.Fatalf("archive: %d %s", r.Code, r.Body.String())
	}
	// The first remote member's upstream does not hold the record, so the
	// next one's answers.
	f = remoteMembers(t, map[string]string{"example.com": "", "other.example": second}, data,
		"puppet-local", "other")
	wantBody(t, f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "admin"),
		withSum(record(`["second"]`)))
	if r := f.do("HEAD", v+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"); r.Code != 200 ||
		r.Body.Len() != 0 {
		t.Fatalf("HEAD archive: %d %s", r.Code, r.Body.String())
	}
	// No member's upstream holds it.
	f = remoteMembers(t, map[string]string{"example.com": "", "other.example": ""}, data,
		"puppet-local", "other", "hosted")
	wantError(t, f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "admin"), 404,
		"Puppet release not found")
	wantError(t, f.do("GET", v+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"), 404,
		"Puppet release not found")

	// A hosted member's release of the version is listed, so it is the one
	// answered, whatever the remote members' records say.
	f = remoteMembers(t, map[string]string{"example.com": first}, data, "puppet-local", "hosted")
	hosted, err := f.db.Repos().Get(t.Context(), "hosted")
	if err != nil {
		t.Fatal(err)
	}
	rel := f.deployRelease(hosted, "acme-widget", "2.0.0", moduleArchive(t, "acme-widget", "2.0.0"))
	wantBody(t, f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "admin"), releaseJSON(rel)+"\n")
	if r := f.do("GET", v+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"); r.Code != 200 ||
		!bytes.Equal(r.Body.Bytes(), rel.data) {
		t.Fatalf("hosted archive: %d %s", r.Code, r.Body.String())
	}
}

// A virtual repository passes over a remote member the caller may not read
// the module of, one whose record read answers 404, as a member whose
// patterns reject it does, and, for the archive, one whose patterns reject
// the archive; a member's other failure fails the answer.
func TestUnitVirtualRecordMembersPassedOver(t *testing.T) {
	const v = "/artifactory/api/puppet/virtual"
	data := linkedArchive(t, "2.0.0")
	second := recordOf(t, "2.0.0", data, "second")
	t.Run("unreadable member", func(t *testing.T) {
		f := remoteMembers(t, map[string]string{"example.com": second, "other.example": second}, data,
			"puppet-local", "other")
		f.grant(map[string]accesstest.Target{"other": {}, "virtual": {}})
		if r := f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "viewer"); r.Code != 200 ||
			!strings.Contains(r.Body.String(), `"tags":["second"]`) {
			t.Fatalf("release: %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("archive refused by a member", func(t *testing.T) {
		first := recordOf(t, "2.0.0", data, "first")
		f := remoteMembers(t, map[string]string{"example.com": first, "other.example": second}, data,
			"puppet-local", "other")
		c, err := f.db.Repos().Get(t.Context(), "puppet-local")
		if err != nil {
			t.Fatal(err)
		}
		c.ExcludesPattern = new("**/*.tar.gz")
		f.save(c)
		// The record is still the first member's: its patterns reject only
		// the archive (R3, R5).
		if r := f.do("GET", v+"/v3/releases/acme-widget-2.0.0", "admin"); r.Code != 200 ||
			!strings.Contains(r.Body.String(), `"tags":["first"]`) {
			t.Fatalf("release: %d %s", r.Code, r.Body.String())
		}
		if r := f.do("GET", v+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"); r.Code != 200 ||
			!bytes.Equal(r.Body.Bytes(), data) {
			t.Fatalf("archive: %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("failures", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			err     error
			status  int
			message string
		}{
			{"refused", format.Rejectf(404, "refused"), 404, "Puppet release not found"},
			{"upstream 503", &source.HTTPError{Status: 503}, 503, "Service Unavailable"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := tunedMembers(t, func(s *metadataSource) { s.release.readError = tc.err },
					map[string]string{"example.com": second}, data, "puppet-local")
				for _, route := range []string{
					"/v3/releases/acme-widget-2.0.0", "/v3/files/acme-widget-2.0.0.tar.gz",
				} {
					wantError(t, f.do("GET", v+route, "admin"), tc.status, tc.message)
				}
			})
		}
	})
}

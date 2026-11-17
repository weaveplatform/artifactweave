package puppethttp_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// captureLog sends the default logger, which a kit without a Logger
// warns through, to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	logs := new(bytes.Buffer)
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return logs
}

// A virtual repository walks nested virtual members (decision 0008, R2):
// each real member once, a cycle back to itself or to the outer virtual
// repository skipped, and a member reached twice kept at its first place.
// main answered 400 "invalid virtual repository member".
func TestAccPuppetVirtualWalksNestedMembersOnce(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.deployVersion(f.c, "1.0.0", variantArchive(t, "1.0.0", "local"))
	other := f.create(repo.Config{Key: "other", Class: repo.Local, PackageType: repo.Puppet})
	f.deployVersion(other, "2.0.0", variantArchive(t, "2.0.0", "other"))
	f.create(repo.Config{
		Key: "inner", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{"other", "virtual", "inner"},
	})
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{"inner", "virtual", "puppet-local", "inner", "other"},
	})
	got := releaseVersions(t, f.do("GET", virtualBase+"/v3/releases?module=acme-widget", "admin"))
	if len(got) != 2 || got["1.0.0"] == "" || got["2.0.0"] == "" {
		t.Fatalf("releases through nested members = %v", got)
	}
	for _, path := range []string{
		"/v3/files/acme-widget-1.0.0.tar.gz", "/v3/files/acme-widget-2.0.0.tar.gz",
		"/v3/releases/acme-widget-2.0.0", "/v3/modules/acme-widget", "/v3/modules?query=acme-widget",
	} {
		if r := f.do("GET", virtualBase+path, "admin"); r.Code != 200 {
			t.Fatalf("%s through nested members: %d %s", path, r.Code, r.Body)
		}
	}
}

// A caller who may read the members alone, with no "ANY" target, resolves
// through the virtual repository, since no permission target can name it
// (decision 0006) and each member decides Read on what it serves; one who
// may read no member gets Puppet's not-found rather than 403 (decision
// 0008, R3).
func TestAccPuppetVirtualServesAMemberOnlyReader(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.deployVersion(f.c, "1.0.0", variantArchive(t, "1.0.0", "local"))
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{"puppet-local"},
	})
	f.grant(map[string]accesstest.Target{"puppet-local": {Include: []string{"**"}}})
	for _, path := range []string{
		"/v3/releases?module=acme-widget", "/v3/releases/acme-widget-1.0.0",
		"/v3/files/acme-widget-1.0.0.tar.gz", "/v3/modules/acme-widget",
	} {
		if r := f.do("GET", virtualBase+path, "viewer"); r.Code != 200 {
			t.Fatalf("%s for a member-only reader: %d %s", path, r.Code, r.Body)
		}
	}
	wantError(t, f.do("GET", virtualBase+"/v3/releases/acme-widget-1.0.0", "stranger"), 404,
		"Puppet release not found")
	wantError(t, f.do("GET", virtualBase+"/v3/files/acme-widget-1.0.0.tar.gz", "stranger"), 404,
		"Puppet release not found")
	wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "stranger"), 404,
		"Puppet module not found")
	if got := releaseVersions(t, f.do("GET", virtualBase+"/v3/releases?module=acme-widget",
		"stranger")); len(got) != 0 {
		t.Fatalf("a caller who may read no member was listed %v", got)
	}
}

// A virtual repository passes over a remote member whose upstream misses,
// and one that fails with a warning naming it [I] (decision 0008, R3), and
// lists its other members' releases.
func TestAccPuppetVirtualPassesOverMissingAndFailingRemotes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		warned bool
	}{{"miss", kernel.ErrNotFound, false}, {"fault", source.ErrUnavailable, true}} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			src := metadataSource{
				readError: tc.err,
				body:      upstreamBody(t, fixtureArchive(t)),
				check:     func(context.Context, string) error { return nil },
			}
			f := newFixtureWith(t, repo.Local,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
			f.deployVersion(f.c, "2.0.0", variantArchive(t, "2.0.0", "local"))
			f.create(repo.Config{
				Key: "remote", Class: repo.Remote, PackageType: repo.Puppet,
				URL: "https://example.com/puppet/",
			})
			f.create(repo.Config{
				Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
				Repositories: []string{"remote", "puppet-local"},
			})
			got := releaseVersions(t, f.do("GET", virtualBase+"/v3/releases?module=acme-widget", "admin"))
			if len(got) != 1 || got["2.0.0"] == "" {
				t.Fatalf("releases = %v", got)
			}
			logged := strings.Contains(logs.String(), "virtual repository passed over a failing member") &&
				strings.Contains(logs.String(), "member=remote") &&
				strings.Contains(logs.String(), "virtual=virtual")
			if logged != tc.warned {
				t.Fatalf("warning logged %v, want %v: %s", logged, tc.warned, logs)
			}
		})
	}
}

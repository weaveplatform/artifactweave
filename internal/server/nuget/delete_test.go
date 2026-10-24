package nuget_test

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// dotnet nuget delete sends DELETE {PackagePublish}/{id}/{version}: a hard
// delete of the version's package and symbols, matched ignoring case and
// normalised, 204 with no body.
func TestAccDelete(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	f.deploy("Acme.Lib.1.0.0.snupkg", snupkg("Acme.Lib", "1.0.0"))
	f.push("Acme.Lib", "2.0.0", "")
	f.push("Acme.Two", "1.0.0", "")
	f.deploy("Acme.Two.1.0.0.snupkg", snupkg("Acme.Two", "1.0.0"))

	rec := f.do(http.MethodDelete, v2+"/acme.lib/1.0", "writer")
	wantStatus(t, rec, http.StatusNoContent)
	if rec.Body.Len() != 0 || f.stored("Acme.Lib.1.0.0.nupkg") ||
		f.stored("Acme.Lib.1.0.0.snupkg") {
		t.Fatalf("not deleted: %s", rec.Body)
	}
	if got := f.flatVersions("Acme.Lib"); len(got) != 1 || got[0] != "2.0.0" {
		t.Fatalf("versions %v", got)
	}
	// Gone, so not found again.
	wantStatus(t, f.do(http.MethodDelete, v2+"/Acme.Lib/1.0.0", "writer"), http.StatusNotFound)

	for _, c := range []struct {
		user, path string
		want       int
	}{
		// A version the caller may not read is not revealed.
		{"nobody", v2 + "/Acme.Lib/2.0.0", http.StatusNotFound},
		{"anonymous", v2 + "/Acme.Lib/2.0.0", http.StatusNotFound},
		{"partial", v2 + "/Acme.Lib/2.0.0", http.StatusNotFound},
		// One the caller may read but not delete is refused.
		{"reader", v2 + "/Acme.Lib/2.0.0", http.StatusForbidden},
		{"deployer", v2 + "/Acme.Lib/2.0.0", http.StatusForbidden},
		// Delete on every file, the symbols too, before any goes.
		{"pkgdeleter", v2 + "/Acme.Two/1.0.0", http.StatusForbidden},
		{"writer", v2 + "/Acme.Lib/9.0.0", http.StatusNotFound},
		{"writer", v2 + "/Acme.None/1.0.0", http.StatusNotFound},
		{"writer", v2 + "/Acme.Lib/not-a-version", http.StatusNotFound},
		// Through a folder, as 7.x answers it (RTFACT-23310), or without a
		// version.
		{"writer", v2 + "/team/Acme.Lib/2.0.0", http.StatusNotFound},
		{"writer", v2 + "/Acme.Lib", http.StatusNotFound},
		{"writer", v2 + "/a;=x/1.0.0", http.StatusBadRequest},
	} {
		rec := f.do(http.MethodDelete, c.path, c.user)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d %s", c.user, c.path, rec.Code, rec.Body)
		}
	}
	if !f.stored("Acme.Two.1.0.0.nupkg") || !f.stored("Acme.Lib.2.0.0.nupkg") {
		t.Fatal("a refused delete removed something")
	}
	// The package alone, which pkgdeleter may delete.
	wantStatus(t, f.do(http.MethodDelete, v2+"/Acme.Lib/2.0.0", "pkgdeleter"),
		http.StatusNoContent)
}

// There is no unlisted state, so a relist answers 200 to a caller who may
// annotate the version, and changes nothing.
func TestAccRelist(t *testing.T) {
	f := newFixture(t)
	f.push("Acme.Lib", "1.0.0", "")
	for _, c := range []struct {
		user, path string
		want       int
	}{
		{"writer", v2 + "/Acme.Lib/1.0.0", http.StatusOK},
		{"admin", v2 + "/acme.lib/1.0", http.StatusOK},
		{"reader", v2 + "/Acme.Lib/1.0.0", http.StatusForbidden},
		{"nobody", v2 + "/Acme.Lib/1.0.0", http.StatusNotFound},
		{"writer", v2 + "/Acme.Lib/2.0.0", http.StatusNotFound},
		{"writer", v2 + "/Acme.Lib", http.StatusNotFound},
	} {
		rec := f.do(http.MethodPost, c.path, c.user)
		if rec.Code != c.want || c.want == http.StatusOK && rec.Body.Len() != 0 {
			t.Errorf("%s %s: %d %s", c.user, c.path, rec.Code, rec.Body)
		}
	}
	if got := f.flatVersions("Acme.Lib"); len(got) != 1 {
		t.Fatalf("versions %v", got)
	}
}

// A read the delete needs that fails is a fault.
func TestAccDeleteFaults(t *testing.T) {
	for _, failing := range []string{"version", "files", "byid"} {
		f := newFixture(t, func(st store.Store) store.Store {
			return failingStore{Store: st, failing: failing}
		})
		f.push("Acme.Lib", "1.0.0", "")
		wantStatus(t, f.do(http.MethodDelete, v2+"/Acme.Lib/1.0.0", "writer"),
			http.StatusInternalServerError)
		if len(f.faults) == 0 {
			t.Errorf("%s: no fault logged", failing)
		}
	}
	// The item service refuses: the file went between the lookup and the
	// delete.
	f := newFixture(t, func(st store.Store) store.Store {
		return failingStore{Store: st, failing: "item"}
	})
	f.push("Acme.Lib", "1.0.0", "")
	wantStatus(t, f.do(http.MethodDelete, v2+"/Acme.Lib/1.0.0", "writer"),
		http.StatusInternalServerError)
}

// With forceNugetAuthentication on, every anonymous request to the
// repository's NuGet routes answers 401 with the challenge before it is
// routed, whatever the anonymous user may read; credentials go through.
func TestAccForceAuthentication(t *testing.T) {
	f := newFixture(t)
	forced := "/artifactory/api/nuget/v3/nuget-forced"
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, forced + "/index.json"},
		{http.MethodGet, forced + "/flatcontainer/acme.lib/index.json"},
		{http.MethodGet, forced + "/query"},
		{http.MethodPut, forced + "/symbols"},
		{http.MethodGet, forced + "/nope"},
		{http.MethodPut, "/artifactory/api/nuget/nuget-forced/"},
		{http.MethodDelete, "/artifactory/api/nuget/nuget-forced/Acme.Lib/1.0.0"},
	} {
		for _, user := range []string{"anonymous", ""} {
			rec := f.do(c.method, c.path, user)
			if rec.Code != http.StatusUnauthorized ||
				rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
				t.Errorf("%s %s as %q: %d %v", c.method, c.path, user, rec.Code, rec.Header())
			}
		}
	}
	wantStatus(t, f.do(http.MethodGet, forced+"/index.json", "reader"), http.StatusOK)
	wantStatus(t, f.do(http.MethodGet, forced+"/index.json", "nobody"), http.StatusOK)
	// Without it, anonymous reaches the feed, which shows what it may read.
	wantStatus(t, f.do(http.MethodGet, v3+"/index.json", "anonymous"), http.StatusOK)
}

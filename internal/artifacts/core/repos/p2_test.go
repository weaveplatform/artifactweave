package repos

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitPlanP2Virtual(t *testing.T) {
	known := []repo.Config{
		{Key: "p2-local", Class: repo.Local, PackageType: repo.Generic},
		{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven},
		{Key: "gradle-local", Class: repo.Local, PackageType: repo.Gradle},
		{
			Key:         "generic-remote",
			Class:       repo.Remote,
			PackageType: repo.Generic,
			URL:         "https://taken.example/",
		},
		{
			Key:         "eclipse",
			Class:       repo.Remote,
			PackageType: repo.P2,
			URL:         "https://download.eclipse.org/",
		},
		{Key: "taken.example", Class: repo.Local, PackageType: repo.Generic},
		{Key: "Taken.Example-1", Class: repo.Local, PackageType: repo.Generic},
	}
	type remote struct{ key, url string }
	for _, tc := range []struct {
		name    string
		members []string
		urls    []string
		want    []string
		created []remote
		bad     string
	}{
		{
			name:    "a member remote covers the site",
			members: []string{"eclipse"}, urls: []string{"https://download.eclipse.org/releases/luna"},
			want: []string{"eclipse"},
		},
		{
			// 7.x's UI matches every remote's url, a member or not.
			name: "a stored remote outside the members covers the site",
			urls: []string{"https://download.eclipse.org/releases/luna", "https://download.eclipse.org/releases/juno"},
			want: []string{"eclipse"},
		},
		{
			name:    "a site no remote covers creates one for its host",
			urls:    []string{"https://Plugins.Example.org/updates/2024", "https://plugins.example.org/updates/2025"},
			want:    []string{"plugins.example.org"},
			created: []remote{{"plugins.example.org", "https://plugins.example.org/"}},
		},
		{
			// The remote's url is the host's root, which covers the bare host.
			name:    "a bare host",
			urls:    []string{"https://hostonly.example", "https://hostonly.example/site"},
			want:    []string{"hostonly.example"},
			created: []remote{{"hostonly.example", "https://hostonly.example/"}},
		},
		{
			name:    "a port joins the key (RTFACT-30256)",
			urls:    []string{"http://repo.example:8081/p2/site"},
			want:    []string{"repo.example-8081"},
			created: []remote{{"repo.example-8081", "http://repo.example:8081/"}},
		},
		{
			// Keys are unique without case, of any class and package type.
			name:    "a taken key moves to the next free number",
			urls:    []string{"https://taken.example/site"},
			want:    []string{"taken.example-2"},
			created: []remote{{"taken.example-2", "https://taken.example/"}},
		},
		{
			name:    "a remote may not take the virtual repository's own key",
			urls:    []string{"https://p2-virtual/site"},
			want:    []string{"p2-virtual-1"},
			created: []remote{{"p2-virtual-1", "https://p2-virtual/"}},
		},
		{
			name:    "a url must start the site at a path boundary",
			urls:    []string{"https://download.eclipse.org.mirror/site"},
			want:    []string{"download.eclipse.org.mirror"},
			created: []remote{{"download.eclipse.org.mirror", "https://download.eclipse.org.mirror/"}},
		},
		{
			// Only a remote whose url is the archive's entry serves its files.
			name:    "a site archive gets a remote of its own",
			urls:    []string{"https://download.eclipse.org/site.zip!/"},
			want:    []string{"download.eclipse.org"},
			created: []remote{{"download.eclipse.org", "https://download.eclipse.org/site.zip!/"}},
		},
		{
			name:    "a local site adds its repository once",
			members: []string{"maven-local"},
			urls:    []string{"local://p2-local", "local://p2-local/nested", "local://maven-local"},
			want:    []string{"maven-local", "p2-local"},
		},
		{
			name: "members not named stay, in their order",
			// A member that does not exist is CheckMembers' to refuse.
			members: []string{"maven-local", "ghost", "eclipse"},
			urls:    []string{"https://new.example/site"},
			want:    []string{"maven-local", "ghost", "eclipse", "new.example"},
			created: []remote{{"new.example", "https://new.example/"}},
		},
		{
			name: "an invalid URL", urls: []string{"https://download.eclipse.org/releases/luna", "ftp://x.example/site"},
			bad: "Invalid P2 URL 'ftp://x.example/site': expected local://<key>[/<suffix>] or an http or https URL",
		},
		{
			name: "a local URL naming no repository", urls: []string{"local://missing"},
			bad: "P2 URL 'local://missing' names no local Generic or Maven repository",
		},
		{
			name: "a local URL naming a remote", urls: []string{"local://generic-remote"},
			bad: "P2 URL 'local://generic-remote' names no local Generic or Maven repository",
		},
		{
			name: "a local URL naming another package type", urls: []string{"local://gradle-local"},
			bad: "P2 URL 'local://gradle-local' names no local Generic or Maven repository",
		},
	} {
		v := repo.Config{
			Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: tc.members, P2URLs: tc.urls,
		}
		got, created, err := PlanP2Virtual(v, known)
		if tc.bad != "" {
			var e *apierror.Error
			if !errors.As(err, &e) || e.Status != http.StatusBadRequest || e.Message != tc.bad {
				t.Errorf("%s: %v, want 400 %q", tc.name, err, tc.bad)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !slices.Equal(got.Repositories, tc.want) {
			t.Errorf("%s: members %q, want %q", tc.name, got.Repositories, tc.want)
		}
		if len(created) != len(tc.created) {
			t.Errorf("%s: created %+v, want %+v", tc.name, created, tc.created)
			continue
		}
		for i, c := range created {
			if c.Key != tc.created[i].key || c.URL != tc.created[i].url || c.Class != repo.Remote ||
				c.PackageType != repo.P2 || !c.ListRemoteFolderItems.Bool() || c.ProjectKey != "" {
				t.Errorf("%s: created %+v, want %+v", tc.name, c, tc.created[i])
			}
		}
		// The caller's members are not changed under it.
		if len(tc.members) > 0 && !slices.Equal(v.Repositories, tc.members) {
			t.Errorf("%s: request members became %q", tc.name, v.Repositories)
		}
	}
}

// A key the repository rules refuse answers the direct create's 400, and
// a repository that is not a P2 virtual one, or has no p2Urls, is left as
// it is.
func TestUnitPlanP2VirtualLeavesOthers(t *testing.T) {
	v := repo.Config{
		Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2,
		P2URLs: []string{"https://[::1]:8443/site"},
	}
	var e *apierror.Error
	if _, _, err := PlanP2Virtual(
		v,
		nil,
	); !errors.As(err, &e) ||
		e.Status != http.StatusBadRequest {
		t.Errorf("refused key: %v", err)
	}
	for _, c := range []repo.Config{
		{Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2},
		{Key: "generic-virtual", Class: repo.Virtual, PackageType: repo.Generic, P2URLs: v.P2URLs},
		{Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2, P2URLs: v.P2URLs},
	} {
		got, created, err := PlanP2Virtual(c, nil)
		if err != nil || created != nil || !slices.Equal(got.Repositories, c.Repositories) {
			t.Errorf("%s: %+v %+v %v", c.Key, got, created, err)
		}
	}
}

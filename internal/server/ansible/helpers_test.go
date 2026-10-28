package ansible

import (
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitUpstreamGalaxyBase(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"public Galaxy", "https://galaxy.ansible.com/", "https://galaxy.ansible.com/api/v3/plugin/ansible/content/published"},
		{"public api", "https://galaxy.ansible.com/api/", "https://galaxy.ansible.com/api/v3/plugin/ansible/content/published"},
		{"explicit v3", "https://private.example/api/v3/", "https://private.example/api/v3"},
		{"private root", "https://private.example/galaxy", "https://private.example/galaxy/api/v3"},
		{"invalid", "http://[", ""},
		{"relative", "/api/v3", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamBase(repo.Config{URL: tc.input}); got != tc.want {
				t.Fatalf("%q want %q", got, tc.want)
			}
		})
	}
}

func TestUnitPaginationBounds(t *testing.T) {
	for _, tc := range []struct {
		name, query   string
		offset, limit int
		bad           bool
	}{
		{"default", "", 0, 100, false},
		{"beyond end", "?offset=99&limit=3", 10, 3, false},
		{"bad offset", "?offset=x", 0, 0, true},
		{"too large", "?offset=100001", 0, 0, true},
		{"negative", "?offset=-1", 0, 0, true},
		{"bad limit", "?limit=101", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, l, err := pagination(httptest.NewRequest("GET", "/"+tc.query, nil), 10)
			if (err != nil) != tc.bad || !tc.bad && (o != tc.offset || l != tc.limit) {
				t.Fatalf("%d %d %v", o, l, err)
			}
		})
	}
}

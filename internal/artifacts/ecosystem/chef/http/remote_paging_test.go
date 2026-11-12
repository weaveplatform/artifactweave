package chefhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// upstreamCatalogPage returns a Supermarket catalog page that says it starts
// at start and holds held cookbooks of total, named from offset, as
// supermarket.chef.io writes it: start, total and items, and no limit.
func upstreamCatalogPage(t *testing.T, start, offset, held, total int, name string) []byte {
	t.Helper()
	items := make([]map[string]string, 0, held)
	for i := range held {
		n := fmt.Sprintf("cookbook-%04d", offset+i)
		if name != "" {
			n = name
		}
		items = append(items, map[string]string{
			"cookbook_name":        n,
			"cookbook_maintainer":  "acme",
			"cookbook_description": "a cookbook",
			"cookbook":             "https://supermarket.example/api/v1/cookbooks/" + n,
		})
	}
	data, err := json.Marshal(map[string]any{"start": start, "total": total, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A remote reads a catalog page with more after it as complete when it
// holds at least the 100 cookbooks asked for, and steps on by what it
// held. A page short of 100 with more after it still answers 502, since
// Supermarket's page states no limit that would explain it, as does every
// other page main refused (#1298).
func TestUnitRemoteCatalogPageSizes(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		start, offset, held, total int
		cookbook                   string
		more, valid                bool
	}{
		// Supermarket serves 100 whatever items asks.
		{"Supermarket's page of 100", 0, 0, 100, 150, "", true, true},
		{"Supermarket's last page", 100, 100, 50, 150, "", false, true},
		{"a page that holds more than asked", 0, 0, 500, 1001, "", true, true},
		{"a page of 1,000 with more after it", 0, 0, 1000, 1001, "", true, true},
		{"a catalog of 100", 0, 0, 100, 100, "", false, true},
		{"a short last page", 0, 0, 2, 2, "", false, true},
		{"an empty catalog", 0, 0, 0, 0, "", false, true},
		{"a last page after a page of 1,000", 1000, 1000, 1, 1001, "", false, true},
		{"short under the page asked", 0, 0, 99, 150, "", false, false},
		{"one cookbook with more after it", 0, 0, 1, 2, "", false, false},
		{"empty with more after it", 0, 0, 0, 150, "", false, false},
		{"a page of more than 1,000", 0, 0, 1001, 2000, "", false, false},
		{"a total over 4,096", 0, 0, 100, 4097, "", false, false},
		{"more than the total", 0, 0, 3, 2, "", false, false},
		{"a start the remote did not ask", 0, 100, 50, 150, "", false, false},
		{"an invalid cookbook name", 0, 0, 1, 1, "bad.name", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := upstreamCatalogPage(t, tc.start, tc.offset, tc.held, tc.total, tc.cookbook)
			doc, more, err := parseCatalog(data, tc.offset)
			if !tc.valid {
				var refused *apierror.Error
				if !errors.As(err, &refused) || refused.Status != 502 ||
					refused.Message != "invalid upstream Chef metadata" {
					t.Fatalf("want 502 invalid upstream Chef metadata, got %v", err)
				}
				return
			}
			if err != nil || more != tc.more || len(doc.Items) != tc.held {
				t.Fatalf("got %d cookbooks, more=%t, err=%v", len(doc.Items), more, err)
			}
		})
	}
}

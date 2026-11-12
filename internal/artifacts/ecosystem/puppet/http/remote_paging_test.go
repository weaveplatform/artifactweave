package puppethttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// upstreamReleasePage returns a Forge release page of acme-widget that
// holds held releases from offset, of total, under a pagination object
// that states limit when it is not 0 and names a next page when next is
// set, as forgeapi.puppet.com writes them.
func upstreamReleasePage(t *testing.T, offset, held, total, limit int, next bool) []byte {
	t.Helper()
	results := make([]upstreamVersion, 0, held)
	for i := range held {
		v := fmt.Sprintf("1.%d.0", total-1-offset-i)
		results = append(results, upstreamVersion{
			Version:    v,
			FileURI:    "/v3/files/acme-widget-" + v + ".tar.gz",
			FileSHA256: strings.Repeat("a", 64),
			FileSize:   100,
			Metadata: upstreamManifest{
				Manifest: puppetfmt.Manifest{Name: "acme-widget", Version: v},
			},
		})
	}
	page := map[string]any{"offset": offset, "total": total, "next": nil}
	if limit != 0 {
		page["limit"] = limit
	}
	if next {
		page["next"] = fmt.Sprintf("/v3/releases?module=acme-widget&limit=%d&offset=%d",
			max(limit, 1), offset+held)
	}
	data, err := json.Marshal(map[string]any{"results": results, "pagination": page})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A remote asks for 100 releases a page and reads a page as complete when
// it holds what was asked, or as many as the upstream's own limit says it
// serves. A page with more after it that is short for no reason the
// upstream states still answers 502, as does every other page main
// refused (#1285).
func TestUnitRemotePageSizes(t *testing.T) {
	const feed = "https://forge.example/v3/releases"
	for _, tc := range []struct {
		name                       string
		offset, held, total, limit int
		next                       bool
		more                       bool
		valid                      bool
	}{
		// The Forge serves 100 whatever limit is asked.
		{"the Forge's page of 100", 0, 100, 150, 100, true, true, true},
		{"the Forge's last page", 100, 50, 150, 100, false, false, true},
		{"a smaller limit the upstream states", 0, 20, 150, 20, true, true, true},
		{"a page that holds more than asked", 0, 500, 1001, 0, true, true, true},
		{"a page of 1,000 with no limit", 0, 1000, 1001, 0, true, true, true},
		{"a module of at most 100 releases", 0, 100, 100, 100, false, false, true},
		{"a short last page with no limit", 0, 2, 2, 0, false, false, true},
		{"short under the limit asked", 0, 99, 150, 100, true, false, false},
		{"short with no limit", 0, 99, 150, 0, true, false, false},
		{"short under the limit stated", 0, 19, 150, 20, true, false, false},
		{"empty with more after it", 0, 0, 150, 0, true, false, false},
		{"empty under a stated limit", 0, 0, 150, 20, true, false, false},
		{"a full page with no next", 0, 100, 150, 100, false, false, false},
		{"a last page that names a next", 100, 50, 150, 100, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := upstreamReleasePage(t, tc.offset, tc.held, tc.total, tc.limit, tc.next)
			got, more, err := parseVersions(data, "acme-widget", feed, tc.offset)
			if !tc.valid {
				var refused *apierror.Error
				if !errors.As(err, &refused) || refused.Status != 502 ||
					refused.Message != "invalid upstream Puppet metadata" {
					t.Fatalf("want 502 invalid upstream Puppet metadata, got %v", err)
				}
				return
			}
			if err != nil || more != tc.more || len(got) != tc.held {
				t.Fatalf("got %d releases, more=%t, err=%v", len(got), more, err)
			}
		})
	}
}

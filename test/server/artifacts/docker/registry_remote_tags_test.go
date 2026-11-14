package docker_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
)

// A remote repository's tags list longer than a page, walked through its
// Links with each page size, and paged after names that are no tag, answers
// every page as the whole list cut there: sorted case-insensitively with
// ties broken by bytes, without repeats, each page and Link byte for byte
// (#1423). The pages after the first read the list the first one decoded.
func TestAccRegistryRemoteTagsListPages(t *testing.T) {
	w := registryWorld(t)
	upstream := []string{"latest", "B", "a", "A", "b", "10", "9", "1.0", "v1.0", "V1.0"}
	upstream = append(upstream, "a", "latest")
	for i := range 30 {
		upstream = append(upstream, fmt.Sprintf("r%02d", (i*7)%30), fmt.Sprintf("R%02d", i))
	}
	w.up.Set("/v2/team/image/tags/list", fakeupstream.Resource{
		Body:   registryJSON(t, map[string]any{"name": "team/image", "tags": upstream}),
		Header: http.Header{"Content-Type": {"application/json"}},
	})
	all := slices.Clone(upstream)
	slices.SortFunc(all, layout.Compare)
	all = slices.Compact(all)
	const base = "/v2/remote/team/image/tags/list"
	page := func(n int, last string) (string, string) {
		rest := all
		if last != "" {
			i, found := slices.BinarySearchFunc(all, last, layout.Compare)
			if found {
				i++
			}
			rest = all[i:]
		}
		link := ""
		if n < len(rest) {
			rest = rest[:n]
			link = "<" + base + "?last=" + rest[n-1] + "&n=" + strconv.Itoa(n) + `>; rel="next"`
		}
		body, _ := json.Marshal(map[string]any{"name": "remote/team/image", "tags": rest})
		return string(body), link
	}
	for _, n := range []int{1, 2, 3, 7, len(all) - 1, len(all), len(all) + 1} {
		target, last := base+"?n="+strconv.Itoa(n), ""
		for {
			got := w.request(http.MethodGet, target, "reader", "")
			body, link := page(n, last)
			if got.Code != http.StatusOK || got.Body.String() != body ||
				got.Header().Get("Link") != link {
				t.Fatalf("%s: %d %s Link %q, want %s Link %q",
					target, got.Code, got.Body, got.Header().Get("Link"), body, link)
			}
			if link == "" {
				break
			}
			target = strings.TrimSuffix(strings.TrimPrefix(link, "<"), `>; rel="next"`)
			last = target[strings.Index(target, "last=")+len("last=") : strings.Index(target, "&")]
		}
	}
	for _, last := range []string{"!", "0", "a0", "Latest", "r05", "R05", "r29", "zz"} {
		target := base + "?n=3&last=" + last
		got := w.request(http.MethodGet, target, "reader", "")
		body, link := page(3, last)
		if got.Code != http.StatusOK || got.Body.String() != body ||
			got.Header().Get("Link") != link {
			t.Fatalf("%s: %d %s Link %q, want %s Link %q",
				target, got.Code, got.Body, got.Header().Get("Link"), body, link)
		}
	}
}

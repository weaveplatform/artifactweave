package golanghttp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// The download policy refuses a version's files one path at a time, as
// Artifactory checks each: a .mod the patterns accept is served where they
// reject the .zip, and a blacked-out repository refuses every stored file
// with the policy's message. A synthesised .info answers as a stored one
// would (RTDEV-60689), and a blacked-out repository refuses the list and
// @latest too, after read permission, while the patterns leave both alone
// (#1237).
func TestAccDownloadPolicy(t *testing.T) {
	f := newFixture(t)
	patch := func(change func(*repo.Config)) {
		t.Helper()
		c, err := f.db.Repos().Get(context.Background(), local)
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	v := base + local + "/" + lib + "/@v/"
	excluded, blackedOut := "**/*.zip", true
	patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	f.run(t, []check{
		{
			"zip refused", "", v + "v1.0.0.zip", "reader", http.StatusNotFound,
			"The repository 'go-local' rejected the artifact 'go-local:" + lib +
				"/@v/v1.0.0.zip' due to its include/exclude pattern settings.",
		},
		{
			"mod served",
			"",
			v + "v1.0.0.mod",
			"reader",
			http.StatusOK,
			"=served " + lib + "/@v/v1.0.0.mod",
		},
		{"list", "", v + "list", "reader", http.StatusOK, "v1.0.0"},
		{"synthesised info", "", v + "v1.0.0.info", "reader", http.StatusOK, `"Version":"v1.0.0"`},
	})
	// A pattern on the .info refuses the stored .info and the one made from
	// the package index alike, and @latest of a module whose latest
	// version has none; the list and the other files are not refused.
	excluded = "**/*.info"
	patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	refusedInfo := func(m, version string) string {
		return "=The repository 'go-local' rejected the artifact 'go-local:" + m + "/@v/" +
			version + ".info' due to its include/exclude pattern settings.\n"
	}
	pre := "example.com/acme/pre"
	f.run(t, []check{
		{"stored info excluded", "", v + "v1.1.0.info", "reader", 404, refusedInfo(lib, "v1.1.0")},
		{
			"synthesised info excluded", "", v + "v1.0.0.info", "reader", 404,
			refusedInfo(lib, "v1.0.0"),
		},
		{
			"synthesised latest excluded", "", base + local + "/" + pre + "/@latest", "reader",
			404, refusedInfo(pre, "v1.0.0-rc.1"),
		},
		{"list kept", "", v + "list", "reader", http.StatusOK, "v1.0.0"},
		{
			"mod kept",
			"",
			v + "v1.0.0.mod",
			"reader",
			http.StatusOK,
			"=served " + lib + "/@v/v1.0.0.mod",
		},
		// Read permission comes first: the .mod a synthesised .info needs.
		{"excluded, unreadable", "", v + "v1.0.0.info", "infos", http.StatusForbidden, ""},
	})
	excluded = ""
	patch(func(c *repo.Config) { c.ExcludesPattern, c.BlackedOut = &excluded, &blackedOut })
	blackedOutMessage := func(p string) string {
		return "=The repository 'go-local' is blacked out and cannot serve artifact 'go-local:" +
			p + "'.\n"
	}
	f.run(t, []check{
		{
			"mod refused", "", v + "v1.0.0.mod", "reader", http.StatusNotFound,
			"The repository 'go-local' is blacked out and cannot serve artifact 'go-local:" + lib +
				"/@v/v1.0.0.mod'.",
		},
		{
			"stored info refused",
			"",
			v + "v1.1.0.info",
			"reader",
			http.StatusNotFound,
			"is blacked out",
		},
		{
			"synthesised info refused", "", v + "v1.0.0.info", "reader", http.StatusNotFound,
			blackedOutMessage(lib + "/@v/v1.0.0.info"),
		},
		{
			"list refused",
			"",
			v + "list",
			"reader",
			http.StatusNotFound,
			blackedOutMessage(lib + "/@v/list"),
		},
		{
			"latest refused", "", base + local + "/" + lib + "/@latest", "reader",
			http.StatusNotFound, blackedOutMessage(lib + "/@latest"),
		},
		{
			"latest.info refused", "", v + "latest.info", "reader", http.StatusNotFound,
			blackedOutMessage(lib + "/@v/latest.info"),
		},
		{
			"case-encoded list refused", "", base + local + "/example.com/!acme/upper/@v/list",
			"reader", http.StatusNotFound, blackedOutMessage("example.com/Acme/upper/@v/list"),
		},
		{"refused, unreadable", "", v + "v1.0.0.mod", "zips", http.StatusForbidden, ""},
		// A caller who may read no version learns nothing of the blackout.
		{"list, nothing readable", "", v + "list", "zips", http.StatusNotFound, "has no version"},
		{"synthesised info, unreadable", "", v + "v1.0.0.info", "infos", http.StatusForbidden, ""},
	})
	blackedOut = false
	patch(func(c *repo.Config) { c.BlackedOut = &blackedOut })
	f.run(t, []check{
		{"list served again", "", v + "list", "reader", http.StatusOK, "v1.0.0"},
		{"info served again", "", v + "v1.0.0.info", "reader", http.StatusOK, `"Version":"v1.0.0"`},
	})
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
}

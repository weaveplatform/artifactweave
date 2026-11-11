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
// with the policy's message. What the package index answers, the list and
// a synthesised .info, is left as it was until #1237 settles derived
// metadata.
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
	})
	excluded = ""
	patch(func(c *repo.Config) { c.ExcludesPattern, c.BlackedOut = &excluded, &blackedOut })
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
		{"synthesised info", "", v + "v1.0.0.info", "reader", http.StatusOK, `"Version":"v1.0.0"`},
		{"refused, unreadable", "", v + "v1.0.0.mod", "zips", http.StatusForbidden, ""},
	})
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
}

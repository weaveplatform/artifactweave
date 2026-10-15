package aql_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/aql/aqltest"
)

// TestCases runs every shared fixture through the reference evaluator; the
// store contract suite runs the same cases through each backend's SQL.
func TestCases(t *testing.T) {
	corpus := aqltest.Corpus()
	for _, c := range aqltest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			if strings.HasPrefix(c.Name, "cli/") && c.Want == nil {
				t.Fatal("CLI fixture without expected rows in aqltest.cliWant")
			}
			q, err := c.Parse()
			if err != nil {
				t.Fatal(err)
			}
			got := aqltest.Paths(aql.SelectItems(q, c.Options, corpus))
			if !slices.Equal(got, c.Want) {
				t.Fatalf("got  %q\nwant %q", got, c.Want)
			}
		})
	}
}

// TestBuildCases runs the builds and build.promotions fixtures through the
// reference evaluator.
func TestBuildCases(t *testing.T) {
	corpus := aqltest.Corpus()
	for _, c := range aqltest.BuildCases() {
		t.Run(c.Name, func(t *testing.T) {
			q, err := c.Parse()
			if err != nil {
				t.Fatal(err)
			}
			got := aqltest.BuildRows(aql.SelectBuilds(q, c.Options, corpus))
			if !slices.Equal(got, c.Want) {
				t.Fatalf("got  %q\nwant %q", got, c.Want)
			}
		})
	}
}

package sql

import (
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// SearchQuery is the query of a page of a search's candidates, with its
// arguments, so a test can ask the engine for its plan.
func SearchQuery(
	s *Store,
	repoKey string,
	terms []string,
	prefix string,
	pg kernel.Page,
) (string, []any, error) {
	return searchQuery(s.Dialect(), repoKey, terms, prefix, pg)
}

// SearchVersionsQuery is the query of the versions of a page of n
// candidates.
var SearchVersionsQuery = searchVersionsQuery

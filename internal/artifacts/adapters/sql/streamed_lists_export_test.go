package sql

import "github.com/weaveplatform/artifactweave/internal/kernel"

// FullPathQuery and DependencyPageQuery are the queries the streamed lists'
// pages send, so the plan tests explain exactly those.
func FullPathQuery(
	s *Store, repoKey string, cached bool, folder string, p kernel.Page,
) (string, []any, error) {
	return items{s}.fullPathQuery(repoKey, cached, folder, p)
}

func DependencyPageQuery(c kernel.Checksums, p kernel.Page) (string, []any, error) {
	return dependencyPageQuery(c, p)
}

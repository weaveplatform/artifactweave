package sql

import "github.com/weaveplatform/artifactweave/internal/artifacts/core/port"

// DockerFolderQuery and DockerImageQuery are the queries the page reader
// sends for q, so the plan tests explain exactly those.
func DockerFolderQuery(s *Store, q port.DockerFolderQuery) (string, []any) {
	return dockerPages{s}.folderQuery(q)
}

func DockerImageQuery(s *Store, q port.DockerImageQuery) (string, []any) {
	return dockerPages{s}.imageQuery(q)
}

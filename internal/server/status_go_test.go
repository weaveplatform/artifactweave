package server

import "github.com/weaveplatform/artifactweave/pkg/repo"

// goRepos are Go's fixture repositories: go-local, go-remote and
// go-virtual.
var goRepos = fixtureRepos("go", repo.Go)

// goStatusPart is Go's share of the status table, which its pull
// requests (#272–#275) fill in.
var goStatusPart = statusPart{}

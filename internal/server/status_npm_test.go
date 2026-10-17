package server

import "github.com/weaveplatform/artifactweave/pkg/repo"

// npmRepos are npm's fixture repositories: npm-local, npm-remote and
// npm-virtual.
var npmRepos = fixtureRepos("npm", repo.NPM)

// npmStatusPart is npm's share of the status table, which its pull
// requests (#286–#294) fill in.
var npmStatusPart = statusPart{}

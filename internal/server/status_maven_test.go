package server

import "github.com/weaveplatform/artifactweave/pkg/repo"

// mavenRepos are Maven's fixture repositories: maven-local, maven-remote and
// maven-virtual.
var mavenRepos = fixtureRepos("maven", repo.Maven)

// mavenStatusPart is Maven's share of the status table, which its pull
// requests (#276–#284) fill in.
var mavenStatusPart = statusPart{}

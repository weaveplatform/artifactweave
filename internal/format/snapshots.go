package format

import "github.com/weaveplatform/artifactweave/pkg/repo"

// MinUniqueSnapshots is the fewest unique builds a maxUniqueSnapshots
// cleanup keeps, whatever the limit: "Artifactory requires that you store
// a minimum of 2 unique snapshots" (JFrog KB, "How does the max unique
// snapshots parameter work", 2025-05-15). So a limit of 1 keeps two;
// whether 7.x refuses a limit of 1 instead is on #216.
const MinUniqueSnapshots = 2

// UniqueSnapshots is how many unique builds of a snapshot c's cleanup
// keeps: 0 when c sets no maxUniqueSnapshots, which keeps every build, and
// otherwise never fewer than MinUniqueSnapshots. Maven's cleanup reads it
// when it runs, so a limit set back to 0 since the deploy that marked a
// package deletes nothing (RTDEV-93991, SaaS 7.164.0). npm ignores the
// limit, as JFrog intends (RTFACT-27078, decision 0034).
func UniqueSnapshots(c repo.Config) int {
	if c.MaxUniqueSnapshots == nil || *c.MaxUniqueSnapshots <= 0 {
		return 0
	}
	return max(*c.MaxUniqueSnapshots, MinUniqueSnapshots)
}

package main

import "strings"

// peerRoots are the trees whose change selects the multi-instance lane
// (acceptance-peers): the scenarios and their harness, and the federation
// module and its server suite, whose behavior the scenarios exercise across
// instances. Each is shared or unmapped for the format selector, so a change
// under them also selects full today; the rule keeps the lane routed if a
// narrower profile ever takes them.
var peerRoots = []string{"test/peers/", "internal/federation/", "test/server/federation/"}

// peersLane reports whether the run selects acceptance-peers: every full
// run, and any other code run that changes a peer root. A documentation run
// never does, so a README under test/peers stays on the docs profile.
func (p plan) peersLane() bool {
	switch p.profile {
	case "full":
		return true
	case "docs":
		return false
	}
	for _, c := range p.changes {
		if peerPath(c.path) || peerPath(c.old) {
			return true
		}
	}
	return false
}

func (p plan) peersReason() string {
	switch {
	case p.profile == "full":
		return "full qualification"
	case p.peersLane():
		return "multi-instance harness or federation module changed"
	default:
		return "no multi-instance input changed"
	}
}

func peerPath(file string) bool {
	for _, root := range peerRoots {
		if strings.HasPrefix(file, root) {
			return true
		}
	}
	return false
}

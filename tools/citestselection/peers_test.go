package main

import (
	"strings"
	"testing"
)

// TestUnitPeersLaneFollowsPeerAndFederationChanges: acceptance-peers runs on
// every full run and on any change to the harness or the federation module;
// a change elsewhere, or to documentation alone, leaves it out. A peer root
// also selects full, so the lane's coverage never joins a format run's
// affected-coverage count.
func TestUnitPeersLaneFollowsPeerAndFederationChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes []change
		want    bool
	}{
		{"harness", []change{{status: "M", path: "test/peers/cluster.go"}}, true},
		{"new scenario", []change{{status: "A", path: "test/peers/pairing_test.go"}}, true},
		{"federation module", []change{{status: "M", path: "internal/federation/core/stream.go"}}, true},
		{"federation server suite", []change{{status: "M", path: "test/server/federation/push_test.go"}}, true},
		{"moved out of the harness", []change{{status: "R100", old: "test/peers/old.go", path: "test/support/new.go"}}, true},
		{"harness guide only", []change{{status: "M", path: "test/peers/README.md"}}, false},
		{"one format", []change{{status: "M", path: "internal/artifacts/ecosystem/npm/http/routes.go"}}, false},
		{"deployment", []change{{status: "M", path: "test/terraform/run.sh"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := classify(tc.changes)
			p.changes = tc.changes
			p.candidate = proposeAffected(tc.changes)
			if p.profile == "full" && p.candidate.profile != "full" {
				p.profile = p.candidate.profile
			}
			if got := p.peersLane(); got != tc.want {
				t.Fatalf("%s profile: peers lane %t, want %t", p.profile, got, tc.want)
			}
			if tc.want && p.profile != "full" {
				t.Fatalf("a peer root selected the %s profile, whose coverage count leaves peers out", p.profile)
			}
			output := githubOutputs(p, kindSelection{})
			if !strings.Contains(output, "\npeers="+boolText(tc.want)+"\n") {
				t.Fatalf("impact outputs lack peers=%t:\n%s", tc.want, output)
			}
		})
	}
	for _, p := range []plan{forceFull(plan{}), selectPlan("", "", false), selectPlan("", "", true)} {
		if !p.peersLane() || p.peersReason() != "full qualification" {
			t.Errorf("%s run (%s) left acceptance-peers out", p.profile, p.reason)
		}
	}
}

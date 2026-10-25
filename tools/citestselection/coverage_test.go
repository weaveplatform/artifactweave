package main

import (
	"strings"
	"testing"
)

func TestUnitParseAffectedCoverageUsesExactStatements(t *testing.T) {
	profile := strings.Join([]string{
		"mode: atomic",
		"github.com/weaveplatform/artifactweave/internal/format/npm/a.go:1.1,2.1 9 1",
		"github.com/weaveplatform/artifactweave/internal/format/npm/a.go:3.1,4.1 1 0",
	}, "\n") + "\n"
	got, err := parseCoverageProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if count := got["github.com/weaveplatform/artifactweave/internal/format/npm"]; count.covered != 9 ||
		count.total != 10 {
		t.Fatalf("statement counts = %+v", count)
	}
}

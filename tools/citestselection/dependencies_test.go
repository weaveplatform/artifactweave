package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestUnitReverseClosureIncludesTransitiveProductionConsumers(t *testing.T) {
	base := modulePath + "/internal/"
	graph := map[string][]string{
		base + "format/npm":   nil,
		base + "server/npm":   {base + "format/npm"},
		base + "server":       {base + "server/npm"},
		base + "app":          {base + "server", base + "format/npm"},
		base + "format/maven": nil,
	}
	got := reverseClosure(graph, []string{base + "format/npm"}, "npm")
	for _, want := range []string{"./internal/format/npm", "./internal/server/npm", "./internal/server", "./internal/app"} {
		if !slices.Contains(got, want) {
			t.Errorf("reverse closure omitted %s: %v", want, got)
		}
	}
	if slices.Contains(got, "./internal/format/maven") {
		t.Fatal("unrelated format selected")
	}
}

func TestUnitSourceImportGraphFailsOnMissingInternalPackage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.go"),
		[]byte(`package example; import "github.com/weaveplatform/artifactweave/internal/missing"`),
		0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceImportGraph(root); err == nil {
		t.Fatal("missing internal import did not fail closed")
	}
}

func TestUnitActualNPMReverseDependencies(t *testing.T) {
	got, err := reverseDependentUnits([]string{modulePath + "/internal/format/npm"}, "npm")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"./internal/format/npm", "./internal/server/npm", "./internal/server", "./internal/app"} {
		if !slices.Contains(got, want) {
			t.Errorf("actual import graph omitted %s: %v", want, got)
		}
	}
}

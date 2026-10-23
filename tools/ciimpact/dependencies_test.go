package main

import (
	"slices"
	"testing"
)

func TestReverseClosureIncludesTransitiveProductionConsumers(t *testing.T) {
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

func TestParseInternalImportsFailsClosed(t *testing.T) {
	if _, err := parseInternalImports("malformed"); err == nil {
		t.Fatal("malformed import graph accepted")
	}
}

func TestActualNPMReverseDependencies(t *testing.T) {
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

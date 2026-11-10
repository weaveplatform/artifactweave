package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUnitReverseClosureIncludesTransitiveProductionConsumers(t *testing.T) {
	base := modulePath + "/internal/"
	migration := modulePath + "/migration/"
	staged := modulePath + "/migration/artifacts/ecosystem/"
	// app reaches npm only through npm/http, which its router mounts.
	graph := map[string][]string{
		staged + "npm":        nil,
		staged + "npm/http":   {staged + "npm"},
		migration + "app":     {staged + "npm/http"},
		base + "format/maven": nil,
	}
	got := reverseClosure(graph, []string{staged + "npm"}, "npm")
	for _, want := range []string{"./internal/artifacts/ecosystem/npm", "./internal/artifacts/ecosystem/npm/http", "./internal/app"} {
		if !slices.Contains(got, want) {
			t.Errorf("reverse closure omitted %s: %v", want, got)
		}
	}
	if slices.Contains(got, "./internal/artifacts/ecosystem/maven") {
		t.Fatal("unrelated format selected")
	}
}

// A format's settings reach app through the composition root's
// configuration package, which app imports, so app's tests are selected
// although app imports no package of the format.
func TestUnitReverseClosureCarriesFormatChangesThroughAppConfig(t *testing.T) {
	migration := modulePath + "/migration/"
	staged := migration + "artifacts/ecosystem/"
	graph := map[string][]string{
		staged + "npm":           nil,
		staged + "maven":         nil,
		migration + "app/config": {staged + "npm"},
		migration + "app":        {migration + "app/config"},
		migration + "artifacts":  {staged + "maven"},
	}
	got := reverseClosure(graph, []string{staged + "npm"}, "npm")
	want := []string{
		"./internal/app",
		"./internal/app/config",
		"./internal/artifacts/ecosystem/npm",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reverse closure = %v, want %v", got, want)
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
	got, err := reverseDependentUnits(
		[]string{modulePath + "/migration/artifacts/ecosystem/npm"},
		"npm",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"./internal/artifacts/ecosystem/npm", "./internal/artifacts/ecosystem/npm/http", "./internal/app"} {
		if !slices.Contains(got, want) {
			t.Errorf("actual import graph omitted %s: %v", want, got)
		}
	}
}

func TestUnitImportGraphIncludesStagedPackages(t *testing.T) {
	root := t.TempDir()
	write := func(file, source string) {
		t.Helper()
		target := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	imports := func(pkg string, paths ...string) string {
		source := "package " + pkg + "\n"
		for _, imported := range paths {
			source += "import _ \"" + modulePath + "/" + imported + "\"\n"
		}
		return source
	}
	write("internal/artifacts/ecosystem/npm/npm.go", imports("npm"))
	write("internal/artifacts/ecosystem/npm/http/routes.go",
		imports("npmhttp", "internal/artifacts/ecosystem/npm"))
	write("internal/artifacts/ecosystem/npm/npm.go",
		imports("npm", "internal/artifacts/ecosystem/npm"))
	write("internal/app/router.go", imports("app", "internal/artifacts/ecosystem/npm/http"))
	write("cmd/artifactweave/main.go", imports("main", "internal/app"))
	write("internal/artifacts/ecosystem/pypi/pypi.go", imports("pypi"))
	// The go command builds neither of these, so their imports are not edges.
	write("migration/.staging/stale.go", imports("stale", "migration/missing"))
	write("internal/artifacts/ecosystem/npm/testdata/fixture.go",
		imports("fixture", "migration/missing"))
	graph, err := sourceImportGraph(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := graph[modulePath+"/migration/app"]; !slices.Equal(
		got, []string{modulePath + "/migration/artifacts/ecosystem/npm/http"},
	) {
		t.Fatalf("composition root's staged import was not recorded: %q", got)
	}
	for _, pkg := range []string{"migration/.staging", "internal/artifacts/ecosystem/npm/testdata"} {
		if _, ok := graph[modulePath+"/"+pkg]; ok {
			t.Errorf("graph includes unbuilt package %s", pkg)
		}
	}
	got := reverseClosure(graph, []string{modulePath + "/migration/artifacts/ecosystem/npm"}, "npm")
	want := []string{
		"./internal/artifacts/ecosystem/npm",
		"./internal/app",
		"./internal/artifacts/ecosystem/npm",
		"./internal/artifacts/ecosystem/npm/http",
	}
	// internal/app imports an npm package but belongs to no format, so the
	// closure stops there and never reaches cmd.
	if !slices.Equal(got, want) {
		t.Fatalf("staged reverse closure = %q, want %q", got, want)
	}
	write("internal/app/missing.go", imports("app", "internal/artifacts/missing"))
	if _, err := sourceImportGraph(root); err == nil ||
		!strings.Contains(err.Error(), "internal/artifacts/missing") {
		t.Fatalf("missing staged import did not fail closed: %v", err)
	}
}

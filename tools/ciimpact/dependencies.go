package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const modulePath = "github.com/weaveplatform/artifactweave"

// The format ownership inventory identifies native scenarios and independent
// server tests. Go's current import graph adds production consumers of changed
// packages, including registration, routing and services; a graph failure
// sends the PR to full qualification.
func reverseDependentUnits(changedPackages []string, owner string) ([]string, error) {
	if len(changedPackages) == 0 {
		return nil, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	graph, err := sourceImportGraph(root)
	if err != nil {
		return nil, err
	}
	for _, pkg := range changedPackages {
		if _, ok := graph[pkg]; !ok {
			return nil, fmt.Errorf("changed Go package %s is absent from the import graph", pkg)
		}
	}
	return reverseClosure(graph, changedPackages, owner), nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no module root for Go import graph")
		}
		dir = parent
	}
}

// Parse committed Go source directly, including OS/build-tag alternatives, so
// the planner does not need a network-dependent module download. Including
// more imports than one runner compiles can only broaden the selected set.
func sourceImportGraph(root string) (map[string][]string, error) {
	graph := map[string][]string{}
	visit := func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(file))
		if err != nil {
			return err
		}
		pkg := modulePath + "/" + filepath.ToSlash(rel)
		if _, ok := graph[pkg]; !ok {
			graph[pkg] = nil
		}
		for _, imp := range parsed.Imports {
			imported, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(imported, modulePath+"/internal/") {
				graph[pkg] = append(graph[pkg], imported)
			}
		}
		return nil
	}
	for _, source := range []string{"api", "cmd", "internal", "pkg"} {
		dir := filepath.Join(root, source)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := filepath.WalkDir(dir, visit); err != nil {
			return nil, fmt.Errorf("parse Go import graph in %s: %w", source, err)
		}
	}
	if len(graph) == 0 {
		return nil, fmt.Errorf("empty Go import graph")
	}
	for pkg, imports := range graph {
		for _, imported := range imports {
			if _, ok := graph[imported]; !ok {
				return nil, fmt.Errorf("%s imports unmapped internal package %s", pkg, imported)
			}
		}
	}
	return graph, nil
}

func reverseClosure(graph map[string][]string, changedPackages []string, owner string) []string {
	seen := map[string]bool{}
	active := map[string]bool{}
	for _, pkg := range changedPackages {
		seen[pkg] = true
		active[pkg] = true
	}
	for updated := true; updated; {
		updated = false
		for pkg, imports := range graph {
			if seen[pkg] {
				continue
			}
			for _, imported := range imports {
				if active[imported] {
					seen[pkg] = true
					if formatPackageOwner(pkg) == owner {
						active[pkg] = true
						updated = true
					}
					break
				}
			}
		}
	}
	units := make([]string, 0, len(seen))
	for pkg := range seen {
		if _, ok := graph[pkg]; ok {
			units = append(units, "./"+strings.TrimPrefix(pkg, modulePath+"/"))
		}
	}
	slices.Sort(units)
	return units
}

func formatPackageOwner(pkg string) string {
	for _, prefix := range []string{modulePath + "/internal/format/", modulePath + "/internal/server/"} {
		if rest, ok := strings.CutPrefix(pkg, prefix); ok {
			return strings.Split(rest, "/")[0]
		}
	}
	return ""
}

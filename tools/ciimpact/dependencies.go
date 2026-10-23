package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	cmd := exec.Command(
		"go",
		"list",
		"-f",
		`{{.ImportPath}}|{{join .Imports ","}}`,
		"./internal/...",
	)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("resolve Go import graph: %w: %s", err, out)
	}
	graph, err := parseInternalImports(string(out))
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

func parseInternalImports(out string) (map[string][]string, error) {
	graph := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, imports, ok := strings.Cut(line, "|")
		if !ok || !strings.HasPrefix(name, modulePath+"/internal/") {
			return nil, fmt.Errorf("invalid Go import graph row %q", line)
		}
		graph[name] = strings.Split(imports, ",")
	}
	if len(graph) == 0 {
		return nil, fmt.Errorf("empty Go import graph")
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

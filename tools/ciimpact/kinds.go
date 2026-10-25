package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// kindSelection is source-based so build-tagged and platform-specific tests
// remain visible to the impact planner. Go still decides which files compile
// on each runner. TestMain and the three reviewed fuzz targets are checked by
// tools/testnames; only runnable tests enter the two named Go lanes here.
type kindSelection struct {
	Unit     []string     `json:"unit_packages"`
	Acc      []string     `json:"acceptance_packages"`
	Fuzz     []string     `json:"fuzz_packages"`
	Selected []selectedGo `json:"selected_tests"`
}

type selectedGo struct {
	Package string `json:"package"`
	Source  string `json:"source"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}

func selectKinds(p plan) (kindSelection, error) {
	var selected kindSelection
	if p.profile != "full" && p.profile != "format" {
		return selected, nil
	}
	root, err := moduleRoot()
	if err != nil {
		return selected, err
	}
	return scanKinds(root, p)
}

func scanKinds(root string, p plan) (kindSelection, error) {
	var selected kindSelection
	unit, acc, fuzz := map[string]bool{}, map[string]bool{}, map[string]bool{}
	rootTests := map[string]bool{}
	if p.profile == "format" && p.candidate.owner != "" {
		inventory, err := loadTestOwnership()
		if err != nil {
			return kindSelection{}, err
		}
		for _, name := range inventory.RootFormatDependencies[p.candidate.owner] {
			rootTests[name] = true
		}
	}
	var err error
	for _, area := range []string{"api", "internal", "pkg", "test", "tools"} {
		err = filepath.WalkDir(
			filepath.Join(root, area),
			func(file string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					if entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(file, "_test.go") {
					return nil
				}
				dir, err := filepath.Rel(root, filepath.Dir(file))
				if err != nil {
					return err
				}
				dir = filepath.ToSlash(dir)
				// Native features own test/acceptance and real-cloud scenarios own
				// test/platform. They have independent selected jobs.
				if dir == "test/acceptance" || dir == "test/platform" {
					return nil
				}
				pkg := "./" + dir
				source := filepath.ToSlash(filepath.Join(dir, entry.Name()))
				if p.profile == "format" && !withinUnits(pkg, p.candidate.units) {
					return nil
				}
				if p.profile == "format" && p.candidate.owner != "" &&
					dir == "internal/server" && !rootTests[entry.Name()] {
					return nil
				}
				parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.AllErrors)
				if err != nil {
					return fmt.Errorf("parse test selection %s: %w", file, err)
				}
				for _, declaration := range parsed.Decls {
					fn, ok := declaration.(*ast.FuncDecl)
					if !ok || fn.Recv != nil {
						continue
					}
					name := fn.Name.Name
					var kind string
					switch {
					case testKindName(name, "TestUnit"):
						unit[pkg], kind = true, "unit"
					case testKindName(name, "TestAcc"):
						acc[pkg], kind = true, "acceptance"
					case testKindName(name, "Fuzz"):
						fuzz[pkg], kind = true, "fuzz seed"
					}
					if kind != "" {
						selected.Selected = append(
							selected.Selected,
							selectedGo{pkg, source, name, kind},
						)
					}
				}
				return nil
			},
		)
		if err != nil {
			return kindSelection{}, fmt.Errorf("select %s tests: %w", area, err)
		}
	}
	for pkg := range unit {
		selected.Unit = append(selected.Unit, pkg)
	}
	for pkg := range acc {
		selected.Acc = append(selected.Acc, pkg)
	}
	for pkg := range fuzz {
		selected.Fuzz = append(selected.Fuzz, pkg)
	}
	slices.Sort(selected.Unit)
	slices.Sort(selected.Acc)
	slices.Sort(selected.Fuzz)
	slices.SortFunc(selected.Selected, func(a, b selectedGo) int {
		if a.Package != b.Package {
			return strings.Compare(a.Package, b.Package)
		}
		if a.Source != b.Source {
			return strings.Compare(a.Source, b.Source)
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(selected.Unit) == 0 {
		return kindSelection{}, fmt.Errorf(
			"%s impact plan selected no TestUnit entry points",
			p.profile,
		)
	}
	if p.profile == "full" && len(selected.Acc) == 0 {
		return kindSelection{}, fmt.Errorf(
			"full impact plan selected no package TestAcc entry points",
		)
	}
	return selected, nil
}

func (s kindSelection) runPattern(profile, kind string) string {
	if profile == "full" {
		if kind == "unit" {
			return "^TestUnit"
		}
		return "^TestAcc"
	}
	names := make([]string, 0, len(s.Selected))
	for _, entry := range s.Selected {
		if entry.Kind == kind {
			names = append(names, regexp.QuoteMeta(entry.Name))
		}
	}
	if len(names) == 0 {
		return "^\\z"
	}
	slices.Sort(names)
	names = slices.Compact(names)
	// Make expands a literal '$' in command-line variable values. RE2's \z
	// anchors the absolute end without that expansion hazard.
	return "^(" + strings.Join(names, "|") + ")\\z"
}

func withinUnits(pkg string, units []string) bool {
	for _, unit := range units {
		if unit == pkg {
			return true
		}
		if prefix, ok := strings.CutSuffix(unit, "/..."); ok &&
			(pkg == prefix || strings.HasPrefix(pkg, prefix+"/")) {
			return true
		}
	}
	return false
}

func testKindName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

func writeSelection(path string, kinds kindSelection) error {
	data, err := json.MarshalIndent(kinds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

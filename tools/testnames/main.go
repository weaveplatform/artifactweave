// Command testnames checks that every Go test entry point declares its kind.
// It parses source files directly, including files excluded by the current
// operating system or build tags.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var roots = []string{"api", "internal", "pkg", "test", "tools"}

// Go reserves TestMain and Fuzz names. They are reviewed exceptions with
// explicit CI owners; new hooks or fuzz targets require a policy update.
var testMainOwners = map[string]string{
	"internal/auth/auth_test.go:TestMain":         "three-OS Go test matrix",
	"test/acceptance/acceptance_test.go:TestMain": "Linux native acceptance",
	"test/platform/harness_test.go:TestMain":      "real-cloud platform scenarios",
}

var fuzzOwners = map[string]string{
	"internal/authz/pattern_test.go:FuzzPatternMatch": "three-OS Go test matrix (seed corpus)",
	"internal/format/nuget/pdb_test.go:FuzzPDBKey":    "three-OS Go test matrix (seed corpus)",
	"pkg/types/scope_test.go:FuzzParseScope":          "three-OS Go test matrix (seed corpus)",
}

type testEntry struct {
	Path    string `json:"path"`
	Package string `json:"package"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}

type exception struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

type inventory struct {
	Unit     []testEntry `json:"unit"`
	Acc      []testEntry `json:"acceptance"`
	TestMain []exception `json:"test_main"`
	Fuzz     []exception `json:"fuzz"`
}

func scan(root string, mains, fuzz map[string]string) (inventory, error) {
	var result inventory
	seenMains := make(map[string]bool)
	seenFuzz := make(map[string]bool)
	var problems []string
	for _, dir := range roots {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.AllErrors)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				name := fn.Name.Name
				key := rel + ":" + name
				switch {
				case name == "TestMain":
					owner, ok := mains[key]
					if !ok {
						problems = append(problems, key+": unowned TestMain hook")
						continue
					}
					seenMains[key] = true
					result.TestMain = append(result.TestMain, exception{rel, name, owner})
				case strings.HasPrefix(name, "Fuzz"):
					owner, ok := fuzz[key]
					if !ok {
						problems = append(problems, key+": unowned fuzz target")
						continue
					}
					seenFuzz[key] = true
					result.Fuzz = append(result.Fuzz, exception{rel, name, owner})
				case kindName(name, "TestUnit"):
					result.Unit = append(result.Unit, testEntry{rel, file.Name.Name, name, "unit"})
				case kindName(name, "TestAcc"):
					result.Acc = append(
						result.Acc,
						testEntry{rel, file.Name.Name, name, "acceptance"},
					)
				case strings.HasPrefix(name, "Test"):
					problems = append(problems, key+": test kind must be TestUnit or TestAcc")
				}
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	for key := range mains {
		if !seenMains[key] {
			problems = append(problems, key+": listed TestMain hook is missing")
		}
	}
	for key := range fuzz {
		if !seenFuzz[key] {
			problems = append(problems, key+": listed fuzz target is missing")
		}
	}
	sort.Slice(result.Unit, func(i, j int) bool { return less(result.Unit[i], result.Unit[j]) })
	sort.Slice(result.Acc, func(i, j int) bool { return less(result.Acc[i], result.Acc[j]) })
	sort.Slice(
		result.TestMain,
		func(i, j int) bool { return result.TestMain[i].Path < result.TestMain[j].Path },
	)
	sort.Slice(
		result.Fuzz,
		func(i, j int) bool { return result.Fuzz[i].Path < result.Fuzz[j].Path },
	)
	sort.Strings(problems)
	if len(problems) != 0 {
		return result, errors.New(strings.Join(problems, "\n"))
	}
	return result, nil
}

func less(a, b testEntry) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Name < b.Name
}

// A boundary is required so TestAccess and TestAccepted do not accidentally
// look like classified acceptance tests.
func kindName(name, prefix string) bool {
	return strings.HasPrefix(name, prefix) && len(name) > len(prefix) &&
		name[len(prefix)] >= 'A' && name[len(prefix)] <= 'Z'
}

func main() {
	root := flag.String("root", ".", "repository root")
	jsonOutput := flag.Bool("json", false, "emit the complete test-kind inventory as JSON")
	flag.Parse()
	result, err := scan(*root, testMainOwners, fuzzOwners)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf(
		"test kinds: %d unit, %d acceptance; exceptions: %d TestMain, %d fuzz\n",
		len(result.Unit),
		len(result.Acc),
		len(result.TestMain),
		len(result.Fuzz),
	)
}

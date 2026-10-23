// servertests selects root internal/server tests for one protocol family.
// The checked-in ownership inventory is an audit boundary: an unclassified
// test file makes selection fail, so the caller must run the full gate.
package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

//go:embed ownership.json
var ownershipJSON []byte

var (
	errNoTests = errors.New("no root server tests found")
	families   = map[string]bool{
		"docker": true, "golang": true, "helm": true, "helmoci": true,
		"luarocks": true, "maven": true, "npm": true, "nuget": true,
		"oci": true, "pypi": true, "terraform": true, "terraformbackend": true,
	}
)

func main() {
	family := flag.String("family", "", "protocol family to run")
	root := flag.String("root", "internal/server", "root server test directory")
	flag.Parse()
	pattern, selected, total, err := selectTests(*root, *family, ownershipJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "root server tests:", selected, "of", total, "selected for", *family)
	fmt.Println(pattern)
}

func selectTests(root, family string, inventory []byte) (string, int, int, error) {
	if !families[family] {
		return "", 0, 0, fmt.Errorf("unknown server test family %q", family)
	}
	owners := map[string]string{}
	if err := json.Unmarshal(inventory, &owners); err != nil {
		return "", 0, 0, fmt.Errorf("parse root server test ownership: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(root, "*_test.go"))
	if err != nil {
		return "", 0, 0, fmt.Errorf("find root server tests: %w", err)
	}
	if len(files) == 0 {
		return "", 0, 0, errNoTests
	}
	if len(files) != len(owners) {
		return "", 0, 0, fmt.Errorf(
			"%w: %d files, %d ownership entries",
			errNoTests,
			len(files),
			len(owners),
		)
	}
	var selected []string
	total := 0
	for _, file := range files {
		name := filepath.Base(file)
		owner, ok := owners[name]
		if !ok || owner != "shared" && owner != "common" && !families[owner] {
			return "", 0, 0, fmt.Errorf("unclassified root server test file %s", name)
		}
		names, err := testsIn(file)
		if err != nil {
			return "", 0, 0, err
		}
		total += len(names)
		if owner == "common" || owner == family {
			selected = append(selected, names...)
		}
	}
	if len(selected) == 0 {
		return "", 0, total, fmt.Errorf("%w for %s", errNoTests, family)
	}
	slices.Sort(selected)
	return "^(" + strings.Join(selected, "|") + ")$", len(selected), total, nil
}

var testName = regexp.MustCompile(`^Test[^a-z]`)

func testsIn(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name != "TestMain" &&
			testName.MatchString(fn.Name.Name) {
			names = append(names, fn.Name.Name)
		}
	}
	return names, nil
}

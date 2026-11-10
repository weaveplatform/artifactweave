package pkgmove

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// watched are the names that a file reaches through one import path.
type watched struct {
	// pkgName is the package's name, by which an import without an explicit
	// name refers to it.
	pkgName string
	names   map[string]bool
}

// watch is what fix looks for in the syntax of a file that no load compiles:
// names that a file directly in a directory uses bare, and names that any
// file uses through an import.
type watch struct {
	bare map[string]map[string]bool
	via  map[string]*watched
}

func newWatch() watch {
	return watch{bare: map[string]map[string]bool{}, via: map[string]*watched{}}
}

// add watches name in the directory dir of module, whose package is pkgName:
// bare in dir, and through the import of dir everywhere else.
func (w watch) add(module, dir, pkgName, name string) {
	if w.bare[dir] == nil {
		w.bare[dir] = map[string]bool{}
	}
	w.bare[dir][name] = true
	w.addVia(module+"/"+dir, pkgName, name)
}

// addVia watches name through the import of pkgPath only.
func (w watch) addVia(pkgPath, pkgName, name string) {
	if w.via[pkgPath] == nil {
		w.via[pkgPath] = &watched{pkgName: pkgName, names: map[string]bool{}}
	}
	w.via[pkgPath].names[name] = true
}

// use returns the first use in f, a file directly in dir, of a watched name,
// as the position and the name as written, or an invalid position.
func (w watch) use(f *ast.File, dir string) (token.Pos, string) {
	local := map[string]map[string]bool{}
	for _, spec := range f.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		if wd := w.via[p]; wd != nil {
			name := explicitName(spec)
			if name == "" {
				name = wd.pkgName
			}
			local[name] = wd.names
		}
	}
	var (
		pos  token.Pos
		what string
	)
	sels := map[*ast.Ident]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.SelectorExpr:
			sels[x.Sel] = true
			if q, ok := x.X.(*ast.Ident); ok && local[q.Name][x.Sel.Name] && !pos.IsValid() {
				pos, what = x.Pos(), q.Name+"."+x.Sel.Name
			}
		case *ast.Ident:
			if w.bare[dir][x.Name] && !sels[x] && !pos.IsValid() {
				pos, what = x.Pos(), x.Name
			}
		}
		return !pos.IsValid()
	})
	return pos, what
}

// uncompiled reports each of files, root-relative Go files, that no load
// compiled (compiled holds those that one did) and whose syntax uses a
// watched name: fix sees no types there, so the use is left for hand work.
// Such a file is built for another system or tagged ignore.
func (t *tree) uncompiled(
	p *fixPlan,
	files []string,
	compiled map[string]*typedFile,
	w watch,
) error {
	slices.Sort(files)
	for _, rel := range slices.Compact(files) {
		if compiled[rel] != nil || !strings.HasSuffix(rel, ".go") || !inPackage(rel) {
			continue
		}
		src, err := t.read(rel)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		// A file that does not parse yields what it can, an empty file at
		// worst; it fails its own build with a clearer message than fix
		// could give.
		f, _ := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if pos, name := w.use(f, path.Dir(rel)); pos.IsValid() {
			p.report(rel, fset.Position(pos).Line,
				"no load compiles this file (GOOS linux, darwin and windows, -tags=e2e), "+
					"so fix leaves its use of %s; rewrite it by hand", name)
		}
	}
	return nil
}

// sourceReaders are the functions whose literal argument names files that a
// test may read from its own package directory.
var sourceReaders = map[string]bool{
	"Glob": true, "ReadDir": true, "ReadFile": true, "Open": true, "OpenFile": true,
	"Stat": true, "Walk": true, "WalkDir": true, "ParseDir": true, "ParseFile": true,
}

// reportSources reports, in the Go files directly in dirs, what a split
// breaks without a compile error: a //go:embed pattern that matches no
// tracked file, and a test that reads its package's own Go source, such as
// filepath.Glob("*.go"), which now sees another set of files.
func (t *tree) reportSources(p *fixPlan, dirs []string) error {
	for _, dir := range dirs {
		for _, rel := range t.goFilesIn(dir) {
			src, err := t.read(rel)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(
				fset,
				rel,
				src,
				parser.ParseComments|parser.SkipObjectResolution,
			)
			if err != nil {
				// The file's own build reports that it does not parse; a
				// directive or call it holds cannot be trusted, so it is left.
				continue
			}
			t.reportEmbeds(p, rel, fset, f)
			if strings.HasSuffix(rel, "_test.go") {
				reportSourceReads(p, rel, fset, f)
			}
		}
	}
	return nil
}

// reportEmbeds reports each //go:embed pattern of f, the file rel, that
// matches no tracked file in its directory.
func (t *tree) reportEmbeds(p *fixPlan, rel string, fset *token.FileSet, f *ast.File) {
	dir := path.Dir(rel)
	var files []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			args, ok := strings.CutPrefix(c.Text, "//go:embed")
			if !ok || (args != "" && args[0] != ' ' && args[0] != '\t') {
				continue
			}
			if files == nil {
				for _, u := range t.under(dir) {
					files = append(files, strings.TrimPrefix(u, dir+"/"))
				}
			}
			for _, pattern := range embedPatterns(args) {
				if !embedMatches(strings.TrimPrefix(pattern, "all:"), files) {
					p.report(
						rel,
						fset.Position(c.Pos()).Line,
						"//go:embed %s matches no file in %s; move what it embeds with an asset file entry, "+
							"or change the pattern",
						pattern,
						dir,
					)
				}
			}
		}
	}
}

// embedPatterns splits the arguments of a //go:embed directive, each bare or
// quoted as a Go string.
func embedPatterns(args string) []string {
	var patterns []string
	for args = strings.TrimSpace(args); args != ""; args = strings.TrimSpace(args) {
		end := strings.IndexAny(args, " \t")
		if args[0] == '"' || args[0] == '`' {
			end = strings.IndexByte(args[1:], args[0]) + 2
		}
		if end <= 0 || end > len(args) {
			end = len(args)
		}
		pattern := args[:end]
		if unquoted, err := strconv.Unquote(pattern); err == nil {
			pattern = unquoted
		}
		patterns = append(patterns, pattern)
		args = args[end:]
	}
	return patterns
}

// embedMatches reports whether pattern matches one of files, paths relative
// to the package directory, or a directory above one of them, which embeds
// everything below it.
func embedMatches(pattern string, files []string) bool {
	for _, f := range files {
		for p := f; p != "."; p = path.Dir(p) {
			if ok, _ := path.Match(pattern, p); ok {
				return true
			}
		}
	}
	return false
}

// reportSourceReads reports each call in the test file f, the file rel, that
// reads Go source from the package directory by a literal name: "." or a
// name or pattern ending in .go, outside testdata.
func reportSourceReads(p *fixPlan, rel string, fset *token.FileSet, f *ast.File) {
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !sourceReaders[sel.Sel.Name] {
			return true
		}
		for _, a := range call.Args {
			lit, ok := a.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, _ := strconv.Unquote(lit.Value)
			if (v == "." || path.Ext(v) == ".go") && !strings.Contains(v, "testdata") {
				p.report(rel, fset.Position(lit.Pos()).Line,
					"the test reads its package's own source (%s %s), which the split changed; "+
						"check that it still sees every file it should", sel.Sel.Name, lit.Value)
			}
		}
		return true
	})
}

// resolve decides the edits and imports of every planned file.
func (p *fixPlan) resolve() {
	for _, rel := range slices.Sorted(maps.Keys(p.files)) {
		p.files[rel].resolve()
	}
}

// reportCycles reports each import that the plan adds and that closes an
// import cycle: the imported package leads back, through the module's
// imports and the other imports the plan adds, to the importing one. An
// external test package is imported by nothing, so it closes no cycle; an
// in-package test closes one through its package.
func (t *tree) reportCycles(p *fixPlan) error {
	type edge struct {
		file     string
		line     int
		from, to string
		test     bool
	}
	var edges []edge
	for _, rel := range slices.Sorted(maps.Keys(p.files)) {
		g := p.files[rel]
		if g.clause != "" || strings.HasSuffix(g.tf.file.Name.Name, "_test") {
			continue
		}
		for _, to := range slices.Sorted(maps.Keys(g.adds)) {
			first := -1
			for off, r := range g.refs {
				if r.pkgPath == to && (first < 0 || off < first) {
					first = off
				}
			}
			edges = append(edges, edge{
				file: rel,
				line: g.tf.line(g.tok.Pos(first)),
				from: g.tf.pkg.Path(),
				to:   to,
				test: strings.HasSuffix(rel, "_test.go"),
			})
		}
	}
	if len(edges) == 0 {
		return nil
	}
	graph, err := t.importGraph()
	if err != nil {
		return err
	}
	for _, e := range edges {
		if !e.test {
			graph[e.from] = append(graph[e.from], e.to)
		}
	}
	short := func(p string) string { return strings.TrimPrefix(p, t.module+"/") }
	for _, e := range edges {
		cycle := importChain(graph, e.to, e.from)
		if cycle == nil {
			continue
		}
		names := []string{short(e.from)}
		for _, c := range cycle {
			names = append(names, short(c))
		}
		p.report(e.file, e.line,
			"importing %s closes an import cycle (%s); break it by hand, with a bridge "+
				"or with an external test (fix -external)",
			short(e.to), strings.Join(names, " -> "))
	}
	return nil
}

// importChain returns the shortest chain of imports from one package to
// another, both included, or nil when there is none.
func importChain(graph map[string][]string, from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			var chain []string
			for p := to; p != ""; p = prev[p] {
				chain = append(chain, p)
			}
			slices.Reverse(chain)
			return chain
		}
		for _, next := range graph[cur] {
			if _, seen := prev[next]; !seen {
				prev[next] = cur
				queue = append(queue, next)
			}
		}
	}
	return nil
}

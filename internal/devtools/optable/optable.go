package optable

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Row is one operation of a bundled spec, as the generated table lists it.
type Row struct {
	Service        string
	Method         string
	Pattern        string
	OperationID    string
	Handwritten    bool
	Cost           string
	ExpensiveQuery []string
	IdempotencyKey bool
	Idempotency    string
	Pagination     string
}

// sharedIdempotencyKey is the parameter an operation references when the
// server honours Idempotency-Key for it (api/src/components-shared.yaml).
const sharedIdempotencyKey = "#/components/parameters/IdempotencyKey"

var (
	errNoSpecs     = errors.New("optable: no bundled specs")
	errNoServer    = errors.New("optable: spec has no servers[0].url")
	errNotMapping  = errors.New("optable: spec is not a mapping")
	errDuplicateOp = errors.New("optable: operation listed twice")
)

// operationMethods are the keys of a path item that hold an operation.
var operationMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// chiParamRE is oapi-codegen's SwaggerUriToChiUri: every path template
// parameter, with its optional style prefix and explode suffix, becomes a
// chi parameter of the same name. The table must give the pattern the
// generated server registers, character for character, or a lookup would
// miss the operation the router serves.
var chiParamRE = regexp.MustCompile(`{[.;?]?([^{}*]+)\*?}`)

// ChiPattern is the route pattern the generated chi server registers for a
// spec path under its server's base URL.
func ChiPattern(base, path string) string {
	return base + chiParamRE.ReplaceAllString(path, "{$1}")
}

// Load reads every api/<service>.openapi.yaml under apiDir and returns its
// operations, sorted by pattern and method.
func Load(apiDir string) ([]Row, error) {
	files, err := filepath.Glob(filepath.Join(apiDir, "*.openapi.yaml"))
	if err != nil {
		return nil, fmt.Errorf("optable: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w in %s", errNoSpecs, apiDir)
	}
	sort.Strings(files)
	var rows []Row
	seen := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("optable: %w", err)
		}
		service := strings.TrimSuffix(filepath.Base(f), ".openapi.yaml")
		got, err := rowsOf(service, data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, r := range got {
			key := r.Method + " " + r.Pattern
			if seen[key] {
				return nil, fmt.Errorf("%w: %s", errDuplicateOp, key)
			}
			seen[key] = true
		}
		rows = append(rows, got...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Pattern != rows[j].Pattern {
			return rows[i].Pattern < rows[j].Pattern
		}
		return rows[i].Method < rows[j].Method
	})
	return rows, nil
}

func rowsOf(service string, data []byte) ([]Row, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("optable: parse: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errNotMapping
	}
	root := doc.Content[0]
	servers := lookup(root, "servers")
	if servers == nil || servers.Kind != yaml.SequenceNode || len(servers.Content) == 0 ||
		lookup(servers.Content[0], "url") == nil {
		return nil, errNoServer
	}
	base := lookup(servers.Content[0], "url").Value
	paths := lookup(root, "paths")
	var rows []Row
	for i := 0; paths != nil && i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		shared := declaresKey(lookup(item, "parameters"))
		for _, m := range operationMethods {
			op := lookup(item, m)
			if op == nil {
				continue
			}
			r := Row{
				Service:        service,
				Method:         strings.ToUpper(m),
				Pattern:        ChiPattern(base, path),
				OperationID:    scalarOf(op, "operationId"),
				Handwritten:    scalarOf(op, "x-handwritten") == "true",
				Cost:           scalarOf(op, "x-cost"),
				Idempotency:    scalarOf(op, "x-idempotency"),
				Pagination:     scalarOf(op, "x-pagination"),
				IdempotencyKey: shared || declaresKey(lookup(op, "parameters")),
			}
			if q := lookup(op, "x-cost-expensive-query"); q != nil && q.Kind == yaml.SequenceNode {
				for _, n := range q.Content {
					r.ExpensiveQuery = append(r.ExpensiveQuery, n.Value)
				}
			}
			rows = append(rows, r)
		}
	}
	return rows, nil
}

func declaresKey(params *yaml.Node) bool {
	if params == nil || params.Kind != yaml.SequenceNode {
		return false
	}
	for _, p := range params.Content {
		if ref := lookup(p, "$ref"); ref != nil && ref.Value == sharedIdempotencyKey {
			return true
		}
	}
	return false
}

func scalarOf(m *yaml.Node, key string) string {
	if v := lookup(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func lookup(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// Render writes the table as the Go source of package pkg: a var Table of
// operations.Operation literals, formatted, with only the fields that are
// set, so a class that is still missing reads as absent rather than as an
// empty string.
func Render(pkg string, rows []Row) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by tools/optable from api/*.openapi.yaml. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString(
		"// Table lists every operation of the bundled specs, sorted by pattern and method.\n",
	)
	b.WriteString("var Table = []Operation{\n")
	for _, r := range rows {
		fields := []string{
			"Service: " + strconv.Quote(r.Service),
			"Method: " + strconv.Quote(r.Method),
			"Pattern: " + strconv.Quote(r.Pattern),
			"OperationID: " + strconv.Quote(r.OperationID),
		}
		if r.Handwritten {
			fields = append(fields, "Handwritten: true")
		}
		for _, f := range []struct{ name, value string }{
			{"Cost", r.Cost}, {"Idempotency", r.Idempotency}, {"Pagination", r.Pagination},
		} {
			if f.value != "" {
				fields = append(fields, f.name+": "+strconv.Quote(f.value))
			}
		}
		if len(r.ExpensiveQuery) > 0 {
			quoted := make([]string, len(r.ExpensiveQuery))
			for i, q := range r.ExpensiveQuery {
				quoted[i] = strconv.Quote(q)
			}
			fields = append(fields, "ExpensiveQuery: []string{"+strings.Join(quoted, ", ")+"}")
		}
		if r.IdempotencyKey {
			fields = append(fields, "IdempotencyKey: true")
		}
		b.WriteString("\t{" + strings.Join(fields, ", ") + "},\n")
	}
	b.WriteString("}\n")
	out, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("optable: format: %w", err)
	}
	return out, nil
}

// Run loads apiDir and writes the table to out, in the package named by
// out's directory, and returns the number of operations written.
func Run(apiDir, out string) (int, error) {
	rows, err := Load(apiDir)
	if err != nil {
		return 0, err
	}
	src, err := Render(filepath.Base(filepath.Dir(out)), rows)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(out, src, 0o600); err != nil {
		return 0, fmt.Errorf("optable: write %s: %w", out, err)
	}
	return len(rows), nil
}

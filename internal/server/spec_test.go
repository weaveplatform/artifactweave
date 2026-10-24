package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/api"
	accessgen "github.com/weaveplatform/artifactweave/internal/server/gen/access"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	distributiongen "github.com/weaveplatform/artifactweave/internal/server/gen/distribution"
	eventgen "github.com/weaveplatform/artifactweave/internal/server/gen/event"
	gridgen "github.com/weaveplatform/artifactweave/internal/server/gen/grid"
	pfedgen "github.com/weaveplatform/artifactweave/internal/server/gen/pfed"
	workergen "github.com/weaveplatform/artifactweave/internal/server/gen/worker"
)

// specFiles is every bundled service spec, so a new service is checked
// without editing the tests.
var specFiles = api.SpecFiles

func loadSpec(t *testing.T, name string) *openapi3.T {
	t.Helper()
	root := moduleRoot(t)
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join(root, "api", name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate %s: %v", name, err)
	}
	return doc
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestOpenAPIFieldDescriptions enforces docs/implementation/api-description-conventions.md:
// every property and parameter is described, and the description opens with
// its presence, matching the schema's required list.
func TestOpenAPIFieldDescriptions(t *testing.T) {
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		var problems []string
		seen := map[*openapi3.Schema]bool{}
		// Scaffolded fragments carry x-draft and TODO(author) placeholders until
		// an author writes the descriptions; they are generated and served (501)
		// but not held to the conventions yet.
		for schemaName, ref := range doc.Components.Schemas {
			if isDraft(ref.Value.Extensions) {
				continue
			}
			checkSchema(name+"#"+schemaName, ref.Value, seen, &problems)
		}
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				if isDraft(op.Extensions) {
					continue
				}
				where := name + " " + method + " " + path
				for _, p := range op.Parameters {
					checkParameter(where, p.Value, &problems)
				}
				if op.RequestBody != nil && op.RequestBody.Value != nil {
					for _, mt := range op.RequestBody.Value.Content {
						if mt.Schema != nil {
							checkSchema(where+" request", mt.Schema.Value, seen, &problems)
						}
					}
				}
				for status, resp := range op.Responses.Map() {
					for _, mt := range resp.Value.Content {
						if mt.Schema != nil {
							checkSchema(where+" response "+status, mt.Schema.Value, seen, &problems)
						}
					}
				}
			}
		}
		sort.Strings(problems)
		for _, p := range problems {
			t.Error(p)
		}
	}
}

func isDraft(ext map[string]any) bool {
	v, ok := ext["x-draft"]
	return ok && v == true
}

func checkSchema(
	where string,
	s *openapi3.Schema,
	seen map[*openapi3.Schema]bool,
	problems *[]string,
) {
	if s == nil || seen[s] {
		return
	}
	seen[s] = true
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	for propName, ref := range s.Properties {
		prop := ref.Value
		want := "Optional within this object."
		if required[propName] {
			want = "Required within this object."
		}
		if !strings.HasPrefix(prop.Description, want) {
			*problems = append(*problems, where+"."+propName+": description must start with "+want)
		}
		checkSchema(where+"."+propName, prop, seen, problems)
	}
	if s.Items != nil {
		checkSchema(where+"[]", s.Items.Value, seen, problems)
	}
	for _, sub := range s.AllOf {
		checkSchema(where, sub.Value, seen, problems)
	}
}

func checkParameter(where string, p *openapi3.Parameter, problems *[]string) {
	presence := "Optional"
	if p.Required {
		presence = "Required"
	}
	want := presence + " " + p.In + " parameter."
	if !strings.HasPrefix(p.Description, want) {
		*problems = append(*problems, where+" param "+p.Name+": description must start with "+want)
	}
}

// TestHandwrittenRoutesNotGenerated asserts that every operation marked
// x-handwritten is excluded from the generated mux (decision 0002), and that
// every other operation is present, so the exclusion cannot silently widen.
func TestHandwrittenRoutesNotGenerated(t *testing.T) {
	type gen struct {
		spec  string
		mount func(chi.Router)
	}
	gens := []gen{
		{"artifactory.openapi.yaml", func(r chi.Router) {
			artgen.HandlerWithOptions(
				artgen.NewStrictHandler(artifactoryService{}, nil),
				artgen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"access.openapi.yaml", func(r chi.Router) {
			accessgen.HandlerWithOptions(
				accessgen.NewStrictHandler(accessService{}, nil),
				accessgen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"event.openapi.yaml", func(r chi.Router) {
			eventgen.HandlerWithOptions(
				eventgen.NewStrictHandler(eventService{}, nil),
				eventgen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"distribution.openapi.yaml", func(r chi.Router) {
			distributiongen.HandlerWithOptions(
				distributiongen.NewStrictHandler(distributionService{}, nil),
				distributiongen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"worker.openapi.yaml", func(r chi.Router) {
			workergen.HandlerWithOptions(
				workergen.NewStrictHandler(workerService{}, nil),
				workergen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"pfed.openapi.yaml", func(r chi.Router) {
			pfedgen.HandlerWithOptions(
				pfedgen.NewStrictHandler(pfedService{}, nil),
				pfedgen.ChiServerOptions{BaseRouter: r},
			)
		}},
		{"grid.openapi.yaml", func(r chi.Router) {
			gridgen.HandlerWithOptions(
				gridgen.NewStrictHandler(gridService{}, nil),
				gridgen.ChiServerOptions{BaseRouter: r},
			)
		}},
	}
	covered := map[string]bool{}
	for _, g := range gens {
		covered[g.spec] = true
	}
	for _, name := range specFiles {
		if !covered[name] {
			t.Errorf("%s has no generated mux in this test; add it so its routes are checked", name)
		}
	}
	for _, g := range gens {
		doc := loadSpec(t, g.spec)
		r := chi.NewRouter()
		g.mount(r)
		checked := 0
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				handwritten := false
				if v, ok := op.Extensions["x-handwritten"]; ok {
					handwritten, _ = v.(bool)
				}
				rctx := chi.NewRouteContext()
				matched := r.Match(rctx, method, path)
				if handwritten && matched {
					t.Errorf(
						"%s %s %s is x-handwritten but the generated mux claims it",
						g.spec,
						method,
						path,
					)
				}
				if !handwritten && !matched {
					t.Errorf(
						"%s %s %s is not x-handwritten but the generated mux lacks it",
						g.spec,
						method,
						path,
					)
				}
				checked++
			}
		}
		if checked == 0 {
			t.Fatalf("%s: no operations checked", g.spec)
		}
	}
}

// TestHandwrittenRoutesServed proves the hand-written operations really are
// served by the full router, so excluding them from generation leaves no gap.
func TestHandwrittenRoutesServed(t *testing.T) {
	h := testHandler()
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		prefix := doc.Servers[0].URL
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				if v, ok := op.Extensions["x-handwritten"]; !ok || v != true {
					continue
				}
				// A native protocol route needs a repository of its own type;
				// these template requests cannot exercise pkgroute dispatch.
				if v, ok := op.Extensions["x-native-protocol"]; ok && v == true {
					continue
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, prefix+path, nil))
				if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
					t.Errorf(
						"%s %s%s: hand-written route not served (%d)",
						method,
						prefix,
						path,
						rec.Code,
					)
				}
			}
		}
	}
}

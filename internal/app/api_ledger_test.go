package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	accesshttp "github.com/weaveplatform/artifactweave/internal/access/adapters/http"
	configurationhttp "github.com/weaveplatform/artifactweave/internal/configuration/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// This ledger joins the served specs, the implementation/status inventory and
// the real mounted router. Updating it is explicit; CI rejects stale evidence.
// The prose policies are reviewed conclusions, not guesses from operation names.
type apiReviewOperation struct {
	Route        string   `json:"route"`
	Spec         string   `json:"spec,omitempty"`
	OperationID  string   `json:"operation_id,omitempty"`
	State        string   `json:"state"`
	Contract     string   `json:"contract,omitempty"`
	Handler      string   `json:"handler"`
	Calls        []string `json:"direct_calls,omitempty"`
	Family       string   `json:"review_family"`
	Effects      string   `json:"effects"`
	Retry        string   `json:"retry"`
	Admission    string   `json:"admission"`
	PageControls []string `json:"declared_page_controls,omitempty"`
	Collections  []string `json:"response_collections,omitempty"`
	Pagination   string   `json:"pagination_execution"`
	Evidence     []string `json:"evidence"`
	// classes is set for a spec operation, and apiReviewDerive takes retry,
	// admission and pagination_execution from it; a hand-written alias has
	// none and keeps the route heuristics.
	classes *apiReviewClasses
}

type apiReviewHandler struct {
	Source string
	Calls  []string
}

func TestAccAPIReliabilityLedger(t *testing.T) {
	root := repopath.Root(t)
	handlers := apiReviewHandlers(t)
	stubbed := stubbedHandwritten(t)
	entries := map[string]apiReviewOperation{}
	for _, spec := range specFiles {
		doc := loadSpec(t, spec)
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				route := method + " " + doc.Servers[0].URL + path
				e := apiReviewOperation{
					Route:       route,
					Spec:        spec,
					OperationID: op.OperationID,
					Contract:    op.Description,
					classes:     apiReviewClassesOf(item, op),
				}
				handwritten, _ := op.Extensions["x-handwritten"].(bool)
				h, hasHandler := handlers[strings.TrimSuffix(spec, ".openapi.yaml")+":"+strings.ToLower(op.OperationID)]
				switch {
				case stubbed[route]:
					e.State, e.Handler = "stub: 501", handwrittenStubHandler
				case handwritten:
					e.State = "partial: native dispatch; supported modes follow the contract"
					e.Handler = "internal/app/router.go: NewRouter -> protocol/handwritten route"
				case implemented[route] && hasHandler:
					e.State, e.Handler, e.Calls = "implemented", h.Source, h.Calls
					if apiReviewPartial(op) {
						e.State = "partial: handler enforces the contract's limitations"
					}
				case implemented[route]:
					e.State = "partial: handwritten override of generated operation"
					e.Handler = "internal/app/router.go: handwritten routes precede generated handlers"
				default:
					e.State = "stub: 501"
					e.Handler = "internal/platform/gen/" + strings.TrimSuffix(
						spec,
						".openapi.yaml",
					) + "/unimplemented.gen.go"
				}
				// The spec's tier is the served contract's own account of the
				// operation; TestAccUnimplementedOperationsAnswer501 holds
				// implemented and stubbed to what the router serves (#1328,
				// #1386). Many generated stubs carry no tier, so only a
				// hand-written one must carry x-tier: stub.
				tier, _ := op.Extensions["x-tier"].(string)
				switch {
				case tier == "stub" && (implemented[route] || handwritten && !stubbed[route]):
					t.Errorf("%s (%s) is served, but %s tiers it x-tier: stub",
						route, op.OperationID, spec)
				case stubbed[route] && tier != "stub":
					t.Errorf(
						"%s (%s) answers 501 from handwrittenStub, but %s does not tier it x-tier: stub",
						route,
						op.OperationID,
						spec,
					)
				}
				for _, p := range append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...) {
					if pageControl(p.Value.Name) {
						e.PageControls = append(
							e.PageControls,
							p.Value.In+" "+p.Value.Name+": "+p.Value.Description,
						)
					}
				}
				for status, response := range op.Responses.Map() {
					if !strings.HasPrefix(status, "2") {
						continue
					}
					for media, content := range response.Value.Content {
						if content.Schema != nil {
							apiReviewCollections(
								status+" "+media,
								content.Schema.Value,
								map[*openapi3.Schema]bool{},
								&e.Collections,
							)
						}
					}
				}
				e.PageControls, e.Collections = sortedReview(
					e.PageControls,
				), sortedReview(
					e.Collections,
				)
				entries[route] = e
			}
		}
	}
	// Include every production alias/dispatch route, including native protocols
	// absent from the seven generated contracts. Do not mount test-only pkgtest.
	router := NewRouter(RouterConfig{}).(chi.Routes)
	err := chi.Walk(
		router,
		func(method, path string, handler http.Handler, _ ...func(http.Handler) http.Handler) error {
			route := method + " " + path
			if _, exists := entries[route]; exists {
				return nil
			}
			fn := runtime.FuncForPC(reflect.ValueOf(handler).Pointer())
			file, _ := fn.FileLine(fn.Entry())
			if file == "<autogenerated>" {
				// Method-value wrappers have no source position; retain their real
				// symbol and the router that mounts them instead of an OS path.
				file = filepath.Join(root, "internal/app/router.go")
			}
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			if strings.Contains(filepath.ToSlash(rel), "internal/platform/gen/") {
				return fmt.Errorf("generated route missing from specs: %s", route)
			}
			if stubbed[route] {
				entries[route] = apiReviewOperation{
					Route:   route,
					State:   "stub: 501",
					Handler: handwrittenStubHandler,
				}
				return nil
			}
			entries[route] = apiReviewOperation{
				Route: route,
				State: "partial: mounted handwritten dispatch; may refuse unsupported paths or modes",
				Handler: filepath.ToSlash(
					rel,
				) + ": " + strings.TrimPrefix(
					fn.Name(),
					"github.com/weaveplatform/artifactweave/",
				),
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]apiReviewOperation, 0, len(entries))
	for _, e := range entries {
		apiReviewPolicy(&e)
		out = append(out, e)
	}
	apiReviewCheckComplete(t, out)
	slices.SortFunc(
		out,
		func(a, b apiReviewOperation) int { return strings.Compare(a.Route, b.Route) },
	)
	apiReviewPagedInStore(t, out)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "docs/research/api-reliability/operations.json")
	if os.Getenv("ARTIFACTWEAVE_UPDATE_API_LEDGER") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("API reliability ledger is stale: review source changes, then run make api-ledger")
	}
	t.Logf(
		"retained %d operations/aliases from %d specs and the production router",
		len(out),
		len(specFiles),
	)
}

// handwrittenStubHandler is the ledger's handler for a route that
// stubbedHandwritten lists.
const handwrittenStubHandler = "internal/app/router.go: handwrittenStub"

func apiReviewPartial(op *openapi3.Operation) bool {
	text := strings.ToLower(op.Description)
	for _, limitation := range []string{"not implemented", "not supported", "unsupported", "ignored", "answers 501", "local repositories only", "requires opt-in"} {
		if strings.Contains(text, limitation) {
			return true
		}
	}
	return false
}

func sortedReview(in []string) []string { sort.Strings(in); return slices.Compact(in) }

// pageControl reports a parameter that pages a list by its name: the
// ledger records it, and the principles guard takes an operation with one
// that answers an array for a collection.
func pageControl(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "page") || strings.Contains(name, "cursor") ||
		strings.Contains(name, "offset") ||
		slices.Contains(
			[]string{"limit", "start_pos", "num_of_rows", "n", "last", "$skip", "$top"},
			name,
		)
}

func apiReviewHandlers(t *testing.T) map[string]apiReviewHandler {
	t.Helper()
	// The operations' handler methods live in the modules' HTTP adapters and
	// in the ecosystems' http packages, named by their paths from the
	// repository root; the router declares none, it only mounts them.
	root := repopath.Root(t)
	var files []string
	for _, dir := range []string{
		"internal/access/adapters/http",
		"internal/artifacts/adapters/http",
		"internal/artifacts/ecosystem/*/http",
		"internal/configuration/adapters/http",
	} {
		found, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	// The artifactory spec's operations are split over partials, which the
	// router's composites embed; each receiver keys its handlers by its spec.
	specOf := map[string]string{
		"ArtifactoryAPI": "artifactory",
		"VCSAPI":         "artifactory",
		"NPMAPI":         "artifactory",
		"DockerAPI":      "artifactory",
		"DebianAPI":      "artifactory",
		"CranAPI":        "artifactory",
		"RPMAPI":         "artifactory",
		"SecurityAPI":    "artifactory",
		"SystemAPI":      "artifactory",
		"AccessService":  "access",
	}
	out := map[string]apiReviewHandler{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			receiver, ok := fn.Recv.List[0].Type.(*ast.Ident)
			if !ok {
				continue
			}
			spec, ok := specOf[receiver.Name]
			if !ok {
				continue
			}
			var calls []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						calls = append(calls, apiReviewExpr(sel.X)+"."+sel.Sel.Name)
					}
				}
				return true
			})
			out[spec+":"+strings.ToLower(fn.Name.Name)] = apiReviewHandler{
				Source: filepath.ToSlash(rel) + ": " + receiver.Name + "." + fn.Name.Name,
				Calls:  sortedReview(calls),
			}
		}
	}
	return out
}

func apiReviewExpr(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return apiReviewExpr(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		return apiReviewExpr(e.Fun) + "()"
	default:
		return "expression"
	}
}

func apiReviewCollections(
	path string,
	s *openapi3.Schema,
	seen map[*openapi3.Schema]bool,
	out *[]string,
) {
	if s == nil || seen[s] {
		return
	}
	seen[s] = true
	defer delete(seen, s)
	if s.Items != nil {
		*out = append(*out, path+"[]")
		return
	}
	for name, child := range s.Properties {
		apiReviewCollections(path+"."+name, child.Value, seen, out)
	}
	for _, child := range s.AllOf {
		apiReviewCollections(path, child.Value, seen, out)
	}
}

func apiReviewPolicy(e *apiReviewOperation) {
	method, path, _ := strings.Cut(e.Route, " ")
	r := &http.Request{Method: method, URL: &url.URL{Path: path}}
	e.Admission = "shared authentication/global/principal; resolved repository/project when applicable; 128 local in-flight; no queue"
	if expensiveRequest(r) {
		e.Admission += "; expensive shared/global/principal rates, 32 local/16 per-principal permits, 2m deadline"
	}
	if strings.Contains(path, "/api/storage/") {
		e.Admission += "; ?list additionally selects expensive policy"
	}
	if configurationhttp.HealthRequest(r) {
		e.Admission = "health exemption"
	}
	if accesshttp.APIRecoveryRequest(r) {
		e.Admission = "administrator recovery reserve: 2 concurrent and 60/min per process; 5s deadline"
	}
	e.Effects = "resource mutation/action; see served contract and handler calls"
	e.Retry = "no key support; reconcile ambiguous completion before retrying"
	switch method {
	case "GET", "HEAD", "OPTIONS":
		e.Effects = "read; downloads may update cache, usage statistics or remote-fetch state"
		e.Retry = "bounded read retry with deadline/backoff and Retry-After; do not send Idempotency-Key"
	case "PUT", "DELETE":
		e.Effects = "resource-addressed write/delete; additional index, retention, audit or lifecycle effects are possible"
		e.Retry = "reconcile the addressed resource; verb alone is not proof of safe retry for every mode"
	}
	if strings.Contains(path, "/move-archives") {
		e.Effects, e.Retry = "CRAN archive move even on GET", "reconcile source/destination; no request deduplication"
	}
	if strings.Contains(path, "/api/build/append/") {
		e.Effects, e.Retry = "append build modules and persist build-info metadata/blob", "optional quoted key: same credential/resource/body, atomic durable receipt, current authorization, 24h retention; otherwise every call appends"
	}
	// The Yum metadata calculation, POST /artifactory/api/yum/{repoKey}, is
	// the one reindex whose path names neither.
	if strings.Contains(path, "/reindex") || strings.Contains(path, "/calculate") ||
		strings.HasPrefix(path, "/artifactory/api/yum/") {
		e.Effects += "; index rebuild/possibly queued work; queue coalescing is not a replay guarantee"
	}
	if path == "/artifactory/api/search/aql" {
		e.Effects, e.Retry = "AQL find reads; items.delete/properties.update also execute through this route", "retry only proven read queries; reconcile actions; no idempotency key"
	}
	if strings.Contains(path, "/api/build/") && method == "DELETE" {
		e.Retry = "latest-run/retention selection can change: reconcile; do not automatically repeat"
	}
	if strings.Contains(path, "/tokens") || strings.Contains(path, "/security/token") ||
		strings.Contains(path, "/oidc/token") {
		if method == "POST" {
			e.Effects, e.Retry = "token issuance/rotation/revocation; secret response", "no replay storage and no key support; reconcile; never blindly repeat issuance/refresh"
		}
	}
	e.Family, e.Pagination, e.Evidence = apiReviewPersistence(path)
	if path == "/artifactory/api/huggingfaceml/{repoKey}/api/artifactweave/migrations/legacy-layout" {
		e.Effects = "administrator-only bounded copy of one legacy revision; atomic item/index publication preserves source bytes/properties/access policy; quiesced writes/configuration required"
		e.Retry = "no key required; repeat the identical mapping with dry_run=true after ambiguous completion: ready permits apply, complete is a no-op; conflicting destinations return 409; reapply never rewinds an advanced ref"
		e.Pagination = "singular migration status; internal cursor scans capped at 1024 tree items and 1024 permission documents; payload verification capped at 16 GiB and streamed"
		e.Evidence = append(
			e.Evidence,
			"internal/artifacts/ecosystem/huggingface/http/migration.go",
			"internal/artifacts/ecosystem/huggingface/http/migration_publish.go",
			"internal/artifacts/ecosystem/huggingface/http/migration_scope_test.go",
			"docs/operations/huggingface-migration.md",
		)
	}
	if method == "POST" && path == "/artifactory/api/bower/{repoKey}/packages" {
		e.Effects = "create immutable repository/name Git registration; virtual routing selects an eligible remote member; duplicate returns native 403"
		e.Retry = "no Idempotency-Key or response replay; repository/name uniqueness prevents duplicates; reconcile registry lookup after ambiguous completion or 403"
		e.Pagination = "singular registration mutation; no collection query"
		e.Evidence = append(
			e.Evidence,
			"internal/artifacts/ecosystem/bower/http/register.go",
			"internal/artifacts/adapters/storetest/bower.go",
		)
	}
	e.Evidence = append(
		e.Evidence,
		"docs/research/decisions/0043-api-reliability.md",
		"internal/app/limits.go",
	)
	if strings.HasPrefix(e.State, "stub:") {
		e.Effects, e.Retry, e.Pagination = "no operation effect; middleware still authenticates/admit-limits", "501 is not transient; do not retry until support changes", "schema-only; no collection query executes"
		e.Evidence = []string{
			e.Handler,
			"internal/app/stub_test.go: TestAccUnimplementedOperationsAnswer501",
		}
	}
	apiReviewDerive(e)
}

// apiReviewClasses are the classes decision 0043 has a spec operation
// declare, read as the guard reads them. A class the operation does not
// declare yet, or still holds as TODO(author), is empty.
type apiReviewClasses struct {
	idempotency, reason, cost, pagination string
	natural, expensiveQuery               []string
	position, size, next                  string
	// keyAccepted: the operation references the shared IdempotencyKey, so
	// the server honors a key there; a key elsewhere answers 400.
	keyAccepted bool
}

func apiReviewClassesOf(item *openapi3.PathItem, op *openapi3.Operation) *apiReviewClasses {
	value := func(name string, allowed []string) string {
		v, _ := op.Extensions[name].(string)
		if slices.Contains(allowed, v) {
			return v
		}
		return ""
	}
	c := &apiReviewClasses{
		idempotency: value("x-idempotency", idempotencyClasses),
		reason:      value("x-idempotency-reason", idempotencyReasons),
		cost:        value("x-cost", costClasses),
		pagination:  value("x-pagination", paginationClasses),
	}
	c.natural, _ = stringList(op.Extensions["x-idempotency-natural"])
	c.expensiveQuery, _ = stringList(op.Extensions["x-cost-expensive-query"])
	c.next, _ = op.Extensions["x-pagination-next"].(string)
	if m, ok := op.Extensions["x-pagination-parameters"].(map[string]any); ok {
		c.position, _ = m["position"].(string)
		c.size, _ = m["size"].(string)
	}
	for _, p := range append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...) {
		if p.Ref == sharedIdempotencyKey {
			c.keyAccepted = true
		}
	}
	return c
}

// The ledger's reading of each idempotency reason (decision 0043,
// "Idempotency classes"): what a client does instead of retrying.
var apiReviewNoneRetry = map[string]string{
	"secret-response":         "the answer carries a credential no replay stores; never blindly repeat; list credentials and revoke any not received",
	"protocol-reconciliation": "reconcile through the protocol's own identifiers and status operations; its clients send no key",
	"external-effect":         "the effect reaches another service, peer, upstream or the file system and cannot roll back; reconcile before repeating",
	"read-or-write":           "retry proven reads only; reconcile writes",
}

// apiReviewDerive takes retry, admission and pagination_execution from the
// operation's declared classes, class by class, in place of the route
// heuristics apiReviewPolicy applied, so the ledger states what the spec
// declares and admission, the key gate and paging will read. A class not
// declared yet leaves its heuristic as it was. The persistence family's
// execution text stays beside a collection's class, since the class says
// how the wire pages, not how the store executes it.
func apiReviewDerive(e *apiReviewOperation) {
	c := e.classes
	if c == nil {
		return
	}
	stub := strings.HasPrefix(e.State, "stub:")
	if text := c.admissionText(); text != "" {
		// A stub is admitted like any operation before it answers 501.
		e.Admission = text
	}
	if class := c.retryClass(); class != "" {
		if stub {
			e.Retry += "; intended " + class
		} else {
			e.Retry = class + ": " + c.retryText()
		}
	}
	switch {
	case stub && c.pagination != "":
		e.Pagination += "; declared " + c.pagination
	case stub:
	case c.pagination == "complete":
		e.Pagination = c.pagingText() + ", " + apiReviewCompleteText(e) + "; " +
			e.Family + ": " + e.Pagination
		if apiReviewStreams(e) {
			e.Evidence = append(e.Evidence, "internal/platform/httpkit/stream.go")
		}
	case c.pagination != "":
		e.Pagination = c.pagingText() + "; " + e.Family + ": " + e.Pagination
	case c.idempotency != "" || c.cost != "":
		// A classified operation without x-pagination is not a collection
		// (the guard holds the ones a schema shows).
		e.Pagination = "not a collection"
	}
}

// apiReviewStreams reports an operation whose handler writes its list
// from store pages as it reads them: it hands the pages to
// httpkit.StreamList (decision 0043, "Streaming complete lists").
func apiReviewStreams(e *apiReviewOperation) bool {
	_, handwritten := apiReviewHandwrittenStreams[e.Route]
	return handwritten || slices.Contains(e.Calls, "httpkit.StreamList")
}

// apiReviewHandwrittenStreams are the handwritten routes that write their
// complete list with httpkit.StreamList: the function that does, which
// apiReviewCheckComplete holds to that call, since the ledger records no
// calls for a handwritten route, and what of the answer streams.
var apiReviewHandwrittenStreams = map[string]struct{ source, text string }{
	"GET /artifactory/api/storage/{repoKey}/{itemPath}": {
		source: "internal/artifacts/adapters/http/storage.go: storageView.list",
		text: "a repository's file list streamed from store pages as they are read, its trees' " +
			"full path walks merged; folder information's children and the trash can's file " +
			"list are collected before they are written",
	},
}

// apiReviewCompleteText says how a served complete operation executes:
// streamed, or collected for the reason apiReviewCollected records.
func apiReviewCompleteText(e *apiReviewOperation) string {
	if h, ok := apiReviewHandwrittenStreams[e.Route]; ok {
		return h.text
	}
	if apiReviewStreams(e) {
		return "streamed from store pages as they are read"
	}
	if reason := apiReviewCollected[e.Route]; reason != "" {
		return "collected before it is written: " + reason
	}
	return "collected before it is written"
}

// apiReviewCollected are the served complete operations that still build
// their whole answer before writing it (#1409), each with why: the list is
// bounded by configuration or by the request, or the answer's order or
// content is known only once every element is read, so writing it as it
// is read would change the bytes.
var apiReviewCollected = map[string]string{
	"GET /access/api/v1/environments":                           "administrator-defined environments, which the store reads whole",
	"GET /access/api/v1/oidc":                                   "administrator OIDC configurations, which the store reads whole",
	"GET /access/api/v1/oidc/{provider_name}/identity_mappings": "one provider's identity mappings, administrator configuration the store reads whole",
	"GET /access/api/v1/roles":                                  "administrator-defined roles, which the store reads whole",
	"GET /artifactory/api/builds/buildArtifacts/{buildName}/{buildNumber}/{buildRepo}": "the files of one build run, which that run's record names",
	"GET /artifactory/api/repositories/configurations":                                 "repository configurations, bounded by configuration and grouped by class",
	"GET /artifactory/api/search/gavc":                                                 "local hits merge with an upstream's answer, deduplicated by path, and the result limit applies after the merge",
	"GET /artifactory/api/search/versions":                                             "versions are ordered newest first in Mercury order, known only once every version is read",
	"GET /artifactory/api/security/keypair":                                            "administrator key pairs, which the store reads whole",
	"GET /artifactory/api/system/security/certificates":                                "administrator certificates, which the store reads whole",
	"GET /artifactory/api/v2/repositories/batch":                                       "bounded by the repository names the request lists",
	"GET /artifactory/api/v2/security/permissions/groups/{groupName}":                  "the permissions naming one principal, which authorization reads whole for every request that principal makes",
	"GET /artifactory/api/v2/security/permissions/users/{userName}":                    "the permissions naming one principal, which authorization reads whole for every request that principal makes",
	"GET /artifactory/api/vcs/branches/{repoKey}/{userOrg}/{repo}":                     "the Git host's reference listing, read whole within the configured reference caps",
	"GET /artifactory/api/vcs/tags/{repoKey}/{userOrg}/{repo}":                         "the Git host's reference listing, read whole within the configured reference caps",
	"POST /artifactory/api/docker/{repo-key}/v2/parentManifests":                       "manifest lists are grouped by digest with every tag holding them, known only once every tag of the image is read",
	"POST /artifactory/api/search/buildArtifacts":                                      "the files of one build run, which that run's record names",
}

// apiReviewCheckComplete holds every served complete operation to the
// rule #1409 settles: it streams from store pages, or apiReviewCollected
// says why it does not. A reason left for an operation that streams, or
// that no longer serves a complete list, is stale and fails too.
func apiReviewCheckComplete(t *testing.T, ops []apiReviewOperation) {
	t.Helper()
	collected := map[string]bool{}
	for i := range ops {
		e := &ops[i]
		if e.classes == nil || e.classes.pagination != "complete" ||
			strings.HasPrefix(e.State, "stub:") {
			continue
		}
		_, reason := apiReviewCollected[e.Route]
		switch streams := apiReviewStreams(e); {
		case streams && reason:
			t.Errorf("%s streams its complete list; drop it from apiReviewCollected", e.Route)
		case !streams && !reason:
			t.Errorf("%s collects its complete list: stream it with httpkit.StreamList "+
				"or record why in apiReviewCollected", e.Route)
		case !streams:
			collected[e.Route] = true
		}
	}
	for route := range apiReviewCollected {
		if !collected[route] {
			t.Errorf("apiReviewCollected names %s, which serves no collected complete list", route)
		}
	}
	for route, h := range apiReviewHandwrittenStreams {
		if !apiReviewCallsStreamList(t, h.source) {
			t.Errorf("%s: %s no longer calls httpkit.StreamList", route, h.source)
		}
	}
}

// apiReviewCallsStreamList reports whether the method source names, as
// "file: Receiver.Method", calls httpkit.StreamList.
func apiReviewCallsStreamList(t *testing.T, source string) bool {
	t.Helper()
	file, method, _ := strings.Cut(source, ": ")
	receiver, name, _ := strings.Cut(method, ".")
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repopath.Root(t), file), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil || fn.Name.Name != name ||
			apiReviewExpr(fn.Recv.List[0].Type) != receiver {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok &&
				apiReviewExpr(sel.X)+"."+sel.Sel.Name == "httpkit.StreamList" {
				calls = true
			}
			return true
		})
	}
	return calls
}

func (c *apiReviewClasses) admissionText() string {
	switch c.cost {
	case "exempt":
		return "exempt: health exemption, no admission"
	case "recovery":
		return "recovery: administrator recovery reserve: 2 concurrent and 60/min per process; 5s deadline"
	case "":
		return ""
	}
	text := c.cost + ": shared authentication/global/principal; resolved repository/project when applicable; 128 local in-flight; no queue"
	if c.cost == "expensive" {
		text += "; expensive shared/global/principal rates, 32 local/16 per-principal permits, 2m deadline"
	}
	for _, q := range c.expensiveQuery {
		text += "; ?" + q + " additionally selects expensive policy"
	}
	return text
}

// retryClass names the idempotency class with what qualifies it: a natural
// operation's identifiers or a none operation's reason.
func (c *apiReviewClasses) retryClass() string {
	switch {
	case c.idempotency == "natural" && len(c.natural) > 0:
		return "natural on " + strings.Join(c.natural, ", ")
	case c.idempotency == "none" && c.reason != "":
		return "none (" + c.reason + ")"
	}
	return c.idempotency
}

func (c *apiReviewClasses) retryText() string {
	switch c.idempotency {
	case "safe":
		return "bounded read retry with deadline/backoff and Retry-After; do not send Idempotency-Key"
	case "natural":
		return "repeating the identical request converges on one state, though the answer may differ; reconcile the target after ambiguous completion; do not send Idempotency-Key"
	case "key":
		if c.keyAccepted {
			return "optional quoted Idempotency-Key: same credential/resource/body replays the committed effect under current authorization for 24h; a duplicate in flight waits up to 30s, then 503 Retry-After; without a key every call repeats the effect"
		}
		return "Idempotency-Key not accepted yet and answers 400; reconcile ambiguous completion before retrying"
	}
	if text := apiReviewNoneRetry[c.reason]; text != "" {
		return "no retry protection; " + text
	}
	return "no retry protection; reconcile before retrying"
}

// pagingText names the paging class with its position, size and next
// pointer as declared.
func (c *apiReviewClasses) pagingText() string {
	if c.pagination == "complete" {
		return "complete: the whole collection in one answer"
	}
	var parts []string
	if c.position != "" {
		parts = append(parts, "position "+c.position)
	}
	if c.size != "" {
		parts = append(parts, "size "+c.size)
	}
	if c.next != "" {
		parts = append(parts, "next "+c.next)
	}
	if len(parts) == 0 {
		return c.pagination
	}
	return c.pagination + " (" + strings.Join(parts, ", ") + ")"
}

func apiReviewPersistence(path string) (string, string, []string) {
	if strings.HasPrefix(path, apiRestrictionPath) || path == apiRestrictionAuditPath {
		return "restriction", "audit: default100/max1000, sequence DESC strict id<cursor; filter+administrator-bound cursor; other operations singular", []string{
			"internal/access/adapters/http/api_restrictions.go",
			"internal/access/adapters/sql/api_restriction.go",
			"internal/access/adapters/storetest/api_restriction.go",
		}
	}
	if path == "/access/api/v2/permissions" {
		return "permission-pages", "GET: default100/max99999, name keyset strictly after the bare-name cursor, principal filter as an indexed grant subquery, LIMIT+1; other methods singular mutations", []string{
			"internal/access/adapters/http/permissions.go",
			"internal/access/core/permission/page.go",
			"internal/access/adapters/sql/permission_page.go",
			"internal/access/adapters/storetest/permission_page.go",
		}
	}
	if path == "/access/api/v2/users" || path == "/access/api/v2/groups" {
		return "identity-pages", "GET: default/max1000, indexed name keyset both directions, SQL filters and LIMIT+1; other methods singular mutations; existing name cursor retained", []string{
			"internal/access/adapters/http/access_identity.go",
			"internal/access/adapters/sql/identity_query.go",
			"internal/access/adapters/storetest/identity_query.go",
		}
	}
	if strings.HasPrefix(path, "/access/") ||
		strings.HasPrefix(path, "/artifactory/api/security/") {
		return "identity", "legacy full lists keep their complete contracts; users, groups, locked users, permissions and tokens are written from store pages; configuration lists collect; source contract declares paged variants", []string{
			"internal/access/adapters/http/access_identity.go",
			"internal/access/adapters/http/tokens.go",
			"internal/access/core/identity/identity.go",
			"internal/access/core/token/token.go",
			"internal/access/adapters/sql/identity.go",
			"internal/access/adapters/sql/projects.go",
		}
	}
	if strings.HasPrefix(path, "/artifactory/api/build") {
		return "build", "build names and numbers retain complete legacy collections, written from store keyset pages; a run's files collect from that run; append/replay and run detail are singular", []string{
			"internal/artifacts/adapters/http/builds.go",
			"internal/artifacts/core/build/build.go",
			"internal/artifacts/adapters/sql/builds.go",
			"internal/app/builds_status_test.go",
		}
	}
	if path == "/artifactory/api/repositories" {
		return "repository-pages", "with limit: class, package type, project and the caller's visibility in SQL, COUNT for X-Total-Count, window by key with LIMIT+1 for the Link next; without limit the whole visible filtered list streamed from windows by key as they are read", []string{
			"internal/artifacts/adapters/http/repositories.go",
			"internal/platform/httpkit/stream.go",
			"internal/artifacts/adapters/sql/repo_page.go",
			"internal/artifacts/adapters/storetest/repo_page.go",
			"internal/access/core/authz/authz.go",
		}
	}
	if strings.HasPrefix(path, "/artifactory/api/repositories") ||
		strings.HasPrefix(path, "/artifactory/api/v2/repositories") {
		return "repository", "legacy arrays remain complete; store keysets are collected and filtered; not bounded by client page size", []string{
			"internal/artifacts/adapters/http/repositories.go",
			"internal/artifacts/core/repos/repos.go",
			"internal/artifacts/adapters/sql/content.go",
		}
	}
	if strings.HasPrefix(path, "/artifactory/api/search/") ||
		strings.HasPrefix(path, "/artifactory/api/versions/") {
		return "search", "AQL retains offset/limit/order/filter semantics and configured result/query caps; convenience searches over stored items and the dependency search write authorized results from batches as they are read, while gavc, versions and build artifacts searches collect; no new keyset promise", []string{
			"internal/artifacts/adapters/http/searches.go",
			"internal/artifacts/adapters/http/aql.go",
			"internal/artifacts/adapters/sql/content_items.go",
			"docs/research/decisions/0012-search-and-aql.md",
		}
	}
	for _, protocol := range []string{"docker", "npm", "nuget", "go", "pypi", "helm", "terraform", "conan", "cran", "conda", "nix", "puppet", "ansible", "chef", "huggingfaceml", "machinelearning", "vcs", "bower", "cocoapods", "vagrant", "gitlfs", "swift", "cargo", "composer", "gems", "deb", "yum", "maven", "lfs", "pods", "pub", "hex", "luarocks", "agentpackages", "agentplugins", "aieditorextensions", "bazelmodules", "jetbrainsplugins", "nimmodel", "skills"} {
		if strings.HasPrefix(path, "/artifactory/api/"+protocol+"/") ||
			protocol == "docker" && strings.HasPrefix(path, "/v2/") {
			return "native-" + protocol, "native wire contract retained: complete indexes, catalog/offset/cursor variants and streaming files differ by route; see family review for exact execution and limits", []string{
				"docs/research/api-reliability/README.md#native-protocols",
				"internal/app/router.go",
				"internal/artifacts/adapters/http/pkgroute/pkgroute.go",
				"internal/artifacts/core/item/item.go",
				"internal/artifacts/adapters/sql/content_items.go",
			}
		}
	}
	return "content-and-configuration", "singular resources, streams or legacy complete collections; declared pagination is retained; no universal keyset/constant-cost claim", []string{
		"internal/app/router.go",
		"internal/artifacts/adapters/http/items.go",
		"internal/artifacts/adapters/http/storage.go",
		"internal/artifacts/adapters/sql/content_items.go",
		"internal/access/adapters/sql/projects.go",
	}
}

// TestUnitAPIReviewDerivesFromClasses: over the guard's valid fixture,
// every classified operation's retry, admission and pagination_execution
// come from its classes, a stub keeps its 501 advice and names its intended
// classes, and an operation still holding TODO(author) keeps the route
// heuristics.
func TestUnitAPIReviewDerivesFromClasses(t *testing.T) {
	doc := principleFixtureDoc(t, validPaths)
	server := doc.Servers[0].URL
	entry := func(method, path, state string) apiReviewOperation {
		t.Helper()
		item := doc.Paths.Value(path)
		if item == nil || item.GetOperation(method) == nil {
			t.Fatalf("no fixture operation %s %s", method, path)
		}
		e := apiReviewOperation{
			Route: method + " " + server + path, State: state,
			classes: apiReviewClassesOf(item, item.GetOperation(method)),
		}
		apiReviewPolicy(&e)
		return e
	}
	heuristic := func(method, path, state string) apiReviewOperation {
		e := apiReviewOperation{Route: method + " " + server + path, State: state}
		apiReviewPolicy(&e)
		return e
	}
	type want struct{ retry, admission, pagination string }
	ordinary := "ordinary: shared authentication/global/principal; resolved repository/project when applicable; 128 local in-flight; no queue"
	expensive := "expensive: shared authentication/global/principal; resolved repository/project when applicable; 128 local in-flight; no queue; expensive shared/global/principal rates, 32 local/16 per-principal permits, 2m deadline"
	_, searchFamily, _ := apiReviewPersistence(server + "/api/search/cursor")
	_, storageFamily, _ := apiReviewPersistence(server + "/api/storage/{repoKey}")
	cases := []struct {
		method, path, state string
		want                want
	}{
		{
			"GET",
			"/api/system/ping",
			"implemented",
			want{
				"safe: bounded read retry with deadline/backoff and Retry-After; do not send Idempotency-Key",
				"exempt: health exemption, no admission",
				"not a collection",
			},
		},
		{
			"GET",
			"/api/artifactweave/api-restrictions/{user}",
			"implemented",
			want{
				admission: "recovery: administrator recovery reserve: 2 concurrent and 60/min per process; 5s deadline",
			},
		},
		{
			"PUT",
			"/api/repositories/{repoKey}",
			"implemented",
			want{
				retry: "natural on path:repoKey, body:/rclass, header:Content-Type: repeating the identical request converges on one state, " +
					"though the answer may differ; reconcile the target after ambiguous completion; do not send Idempotency-Key",
				admission: expensive,
			},
		},
		{
			"POST",
			"/api/build/append/{buildName}",
			"implemented",
			want{
				retry: "key: optional quoted Idempotency-Key: same credential/resource/body replays the committed effect under current " +
					"authorization for 24h; a duplicate in flight waits up to 30s, then 503 Retry-After; without a key every call repeats the effect",
			},
		},
		{
			"POST",
			"/api/security/token",
			"implemented",
			want{
				retry: "none (secret-response): no retry protection; the answer carries a credential no replay stores; never blindly " +
					"repeat; list credentials and revoke any not received",
			},
		},
		{
			"GET",
			"/api/storage/{repoKey}",
			"implemented",
			want{
				admission: ordinary + "; ?list additionally selects expensive policy",
				pagination: "complete: the whole collection in one answer, collected before it is written; " +
					"content-and-configuration: " + storageFamily,
			},
		},
		{"GET", "/api/search/cursor", "implemented", want{
			pagination: "cursor (position cursor, next link); search: " + searchFamily,
		}},
		{
			"GET",
			"/api/search/offset",
			"implemented",
			want{
				pagination: "offset (position offset, size limit, next body:/next); search: " + searchFamily,
			},
		},
		{
			"DELETE",
			"/api/repositories/{repoKey}",
			"stub: 501",
			want{
				retry:      "501 is not transient; do not retry until support changes; intended natural on route",
				admission:  expensive,
				pagination: "schema-only; no collection query executes",
			},
		},
		{"GET", "/api/search/cursor", "stub: 501", want{
			pagination: "schema-only; no collection query executes; declared cursor",
		}},
	}
	for _, c := range cases {
		got := entry(c.method, c.path, c.state)
		for field, pair := range map[string][2]string{
			"retry":      {got.Retry, c.want.retry},
			"admission":  {got.Admission, c.want.admission},
			"pagination": {got.Pagination, c.want.pagination},
		} {
			if pair[1] != "" && pair[0] != pair[1] {
				t.Errorf(
					"%s %s (%s) %s:\n got %q\nwant %q",
					c.method,
					c.path,
					c.state,
					field,
					pair[0],
					pair[1],
				)
			}
		}
	}
	// A complete list whose handler hands its pages to httpkit.StreamList
	// is streamed, and says so.
	item := doc.Paths.Value("/api/storage/{repoKey}")
	streamed := apiReviewOperation{
		Route: "GET " + server + "/api/storage/{repoKey}", State: "implemented",
		Calls:   []string{"httpkit.StreamList"},
		classes: apiReviewClassesOf(item, item.Get),
	}
	apiReviewPolicy(&streamed)
	if want := "complete: the whole collection in one answer, streamed from store pages as they are read; " +
		"content-and-configuration: " + storageFamily; streamed.Pagination != want ||
		!slices.Contains(streamed.Evidence, "internal/platform/httpkit/stream.go") {
		t.Errorf(
			"a streamed complete list: got %q, evidence %v",
			streamed.Pagination,
			streamed.Evidence,
		)
	}
	// TODO(author) on a draft is no class yet: every heuristic stays.
	got, was := entry(
		"GET",
		"/api/scaffolded",
		"stub: 501",
	), heuristic(
		"GET",
		"/api/scaffolded",
		"stub: 501",
	)
	if got.Retry != was.Retry || got.Admission != was.Admission ||
		got.Pagination != was.Pagination {
		t.Errorf("an unclassified operation changed: got %+v, heuristics %+v", got, was)
	}
	// A key operation that does not reference the shared parameter accepts
	// no key yet, and a none without a listed reason still advises.
	c := &apiReviewClasses{idempotency: "key"}
	if got := c.retryText(); !strings.Contains(got, "not accepted yet") {
		t.Errorf("key without the parameter: %q", got)
	}
	c = &apiReviewClasses{idempotency: "none"}
	if got := c.retryClass() + ": " + c.retryText(); got != "none: no retry protection; reconcile before retrying" {
		t.Errorf("none without a reason: %q", got)
	}
	c = &apiReviewClasses{pagination: "native"}
	if got := c.pagingText(); got != "native" {
		t.Errorf("native without companions: %q", got)
	}
}

// apiReviewPagedInStore fails when a served operation that declares a
// cursor, offset or page class still collects its list before slicing it:
// each pages in its store (#1408), so a new one that collects is a
// regression the ledger would otherwise record without complaint.
func apiReviewPagedInStore(t *testing.T, out []apiReviewOperation) {
	t.Helper()
	for _, e := range out {
		paged := strings.HasPrefix(e.Pagination, "cursor") ||
			strings.HasPrefix(e.Pagination, "offset") ||
			strings.HasPrefix(e.Pagination, "page")
		if e.State == "implemented" && paged && strings.Contains(e.Pagination, "collect") {
			t.Errorf("%s declares a paging class but collects its list: %s", e.Route, e.Pagination)
		}
	}
}

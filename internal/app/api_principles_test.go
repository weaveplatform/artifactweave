package app

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	accesshttp "github.com/weaveplatform/artifactweave/internal/access/adapters/http"
	configurationhttp "github.com/weaveplatform/artifactweave/internal/configuration/adapters/http"
)

// The rules of decision 0043, "Rules for every operation", as the guard
// checks them over the bundled specs. Each rule has a fixture in
// api_principles_fixtures_test.go that breaks it.
const (
	ruleClassMissing          = "class-missing"
	ruleClassTodo             = "class-todo"
	ruleClassValue            = "class-value"
	ruleNaturalIdentifier     = "natural-identifier"
	ruleNoneReason            = "none-reason"
	ruleKeyParameter          = "key-parameter"
	ruleKeyServed             = "key-served"
	ruleKeyStub               = "key-stub"
	ruleCostAdmission         = "cost-admission"
	ruleAdmissionResponses    = "admission-responses"
	ruleAdmissionHeaders      = "admission-headers"
	ruleAdmissionEnvelope     = "admission-envelope"
	ruleCollectionPagination  = "collection-pagination"
	ruleCursorNext            = "cursor-next"
	rulePaginationDeclaration = "pagination-declaration"
)

// The class values and idempotency reasons decision 0043 allows, in its
// order; a new reason is added there first.
var (
	idempotencyClasses = []string{"safe", "natural", "key", "none"}
	idempotencyReasons = []string{
		"secret-response", "protocol-reconciliation", "external-effect", "read-or-write",
	}
	costClasses       = []string{"ordinary", "expensive", "exempt", "recovery"}
	paginationClasses = []string{"cursor", "offset", "page", "native", "complete"}
)

// The shared declarations of api/src/components-shared.yaml, as the bundles
// reference them.
const (
	sharedTooManyRequests    = "#/components/responses/TooManyRequests"
	sharedServiceUnavailable = "#/components/responses/ServiceUnavailable"
	sharedIdempotencyKey     = "#/components/parameters/IdempotencyKey"
	idempotencyKeyHeader     = "Idempotency-Key"
)

// sharedRateHeaders maps each admission header to the shared component an
// operation's own 429 or 503 must reference for it.
var sharedRateHeaders = map[string]string{
	"Retry-After":      "#/components/headers/RetryAfter",
	"RateLimit":        "#/components/headers/RateLimit",
	"RateLimit-Policy": "#/components/headers/RateLimitPolicy",
}

// classPlaceholder is what make scaffold writes for each class
// (specoracle.Placeholder); it may stand only on an x-draft operation.
const classPlaceholder = "TODO(author)"

// principleViolation is one operation failing one rule.
type principleViolation struct {
	Rule   string
	Spec   string
	Op     string
	Detail string
}

// principleOp is one operation of a bundled spec, with what the rules need
// to know about it.
type principleOp struct {
	spec, server, method, path string
	item                       *openapi3.PathItem
	op                         *openapi3.Operation
	// served is an operation a handler serves rather than a 501 stub.
	served bool
}

// servedFunc reports whether the router serves an operation, given its
// route as "METHOD server+path".
type servedFunc func(route string, op *openapi3.Operation) bool

func (o principleOp) name() string {
	return o.method + " " + o.server + o.path + " (" + o.op.OperationID + ")"
}

// parameters are the operation's own parameters and those of its path item
// that it does not override, as OpenAPI merges them.
func (o principleOp) parameters() openapi3.Parameters {
	out := append(openapi3.Parameters{}, o.op.Parameters...)
	for _, p := range o.item.Parameters {
		if p.Value != nil && o.op.Parameters.GetByInAndName(p.Value.In, p.Value.Name) == nil {
			out = append(out, p)
		}
	}
	return out
}

func (o principleOp) hasParameter(in, name string) bool {
	for _, p := range o.parameters() {
		if p.Value == nil || p.Value.In != in {
			continue
		}
		if p.Value.Name == name ||
			in == openapi3.ParameterInHeader && strings.EqualFold(p.Value.Name, name) {
			return true
		}
	}
	return false
}

func (o principleOp) hasAnyParameter(name string) bool {
	for _, p := range o.parameters() {
		if p.Value != nil && p.Value.Name == name {
			return true
		}
	}
	return false
}

// examplePath is a request path the operation answers: the server prefix and
// the spec path with every parameter filled in. Admission and the error
// profiles decide from the path's static segments, so any value does.
func (o principleOp) examplePath() string {
	return o.server + pathParameterRE.ReplaceAllString(o.path, "example")
}

var pathParameterRE = regexp.MustCompile(`\{[^{}]+\}`)

// admissionClass is the x-cost the boundary admits a request under today:
// the order limitIngress and limitPrincipal test the request kinds in.
func admissionClass(method, path string, query url.Values) string {
	r := &http.Request{Method: method, URL: &url.URL{Path: path, RawQuery: query.Encode()}}
	switch {
	case configurationhttp.HealthRequest(r):
		return "exempt"
	case accesshttp.APIRecoveryRequest(r):
		return "recovery"
	case expensiveRequest(r):
		return "expensive"
	}
	return "ordinary"
}

// checkAPIPrinciples applies every rule to every operation of doc, the
// bundled spec named spec, of which served picks the operations a handler
// serves.
func checkAPIPrinciples(spec string, doc *openapi3.T, served servedFunc) []principleViolation {
	server := doc.Servers[0].URL
	// The envelope the spec's ErrorResponse describes: the one admission
	// answers the service's own prefix in.
	base := errorProfileFor(server + "/")
	var out []principleViolation
	paths := doc.Paths.InMatchingOrder()
	sort.Strings(paths)
	for _, path := range paths {
		item := doc.Paths.Value(path)
		methods := make([]string, 0, len(item.Operations()))
		for m := range item.Operations() {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		for _, method := range methods {
			o := principleOp{
				spec: spec, server: server, method: method, path: path,
				item: item, op: item.GetOperation(method),
			}
			o.served = served(method+" "+server+path, o.op)
			report := func(rule, format string, args ...any) {
				out = append(out, principleViolation{
					Rule: rule, Spec: spec, Op: o.name(), Detail: fmt.Sprintf(format, args...),
				})
			}
			otherEnvelope := errorProfileFor(o.examplePath()) != base
			checkIdempotency(o, report)
			cost := checkCost(o, report)
			checkAdmissionResponses(o, cost, otherEnvelope, report)
			checkPagination(o, report)
		}
	}
	return out
}

type reportFunc func(rule, format string, args ...any)

// classValue reads one class extension. It reports a missing class when
// required, a placeholder left on a finished operation and a value outside
// allowed, and returns the value only when it is one of them.
func classValue(
	o principleOp,
	ext string,
	allowed []string,
	required bool,
	report reportFunc,
) string {
	raw, ok := o.op.Extensions[ext]
	if !ok {
		if required {
			report(ruleClassMissing, "%s is missing", ext)
		}
		return ""
	}
	v, _ := raw.(string)
	switch {
	case v == classPlaceholder:
		if !isDraft(o.op.Extensions) {
			report(
				ruleClassTodo,
				"%s is still %s on an operation without x-draft",
				ext,
				classPlaceholder,
			)
		}
		return ""
	case !slices.Contains(allowed, v):
		report(ruleClassValue, "%s is %v, not one of %s", ext, raw, strings.Join(allowed, ", "))
		return ""
	}
	return v
}

// stringList reads an extension that holds a list of strings.
func stringList(raw any) ([]string, bool) {
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func checkIdempotency(o principleOp, report reportFunc) {
	class := classValue(o, "x-idempotency", idempotencyClasses, true, report)
	natural, hasNatural := o.op.Extensions["x-idempotency-natural"]
	reason, hasReason := o.op.Extensions["x-idempotency-reason"]
	switch {
	case class == "natural":
		checkNatural(o, natural, hasNatural, report)
	case hasNatural && class != "":
		report(
			ruleNaturalIdentifier,
			"x-idempotency-natural belongs only to a natural operation, not %s",
			class,
		)
	}
	switch {
	case class == "none":
		if r, _ := reason.(string); !slices.Contains(idempotencyReasons, r) {
			report(ruleNoneReason, "x-idempotency-reason is %v, not one of %s",
				reason, strings.Join(idempotencyReasons, ", "))
		}
	case hasReason && class != "":
		report(
			ruleNoneReason,
			"x-idempotency-reason belongs only to a none operation, not %s",
			class,
		)
	}
	for _, p := range o.parameters() {
		if p.Value == nil || p.Value.In != openapi3.ParameterInHeader ||
			!strings.EqualFold(p.Value.Name, idempotencyKeyHeader) {
			continue
		}
		if p.Ref != sharedIdempotencyKey {
			report(ruleKeyParameter, "declares %s itself rather than referencing %s",
				idempotencyKeyHeader, sharedIdempotencyKey)
		}
		if class != "key" && class != "" {
			report(ruleKeyParameter, "declares %s but is %s, not key", idempotencyKeyHeader, class)
		}
	}
	// A served key operation honours the key, which it says by declaring
	// it; a stub answers 501 to any request, so a key it declared would
	// promise a replay that no handler runs.
	declares := o.hasParameter(openapi3.ParameterInHeader, idempotencyKeyHeader)
	switch {
	case class == "key" && o.served && !declares:
		report(ruleKeyServed, "is a served key operation that does not declare %s",
			idempotencyKeyHeader)
	case !o.served && declares:
		report(ruleKeyStub, "declares %s, but a 501 stub serves it", idempotencyKeyHeader)
	}
}

// checkNatural holds a natural operation to naming the request elements
// that identify its target, each one the operation really has.
func checkNatural(o principleOp, raw any, present bool, report reportFunc) {
	entries, ok := stringList(raw)
	if !present || !ok || len(entries) == 0 {
		report(
			ruleNaturalIdentifier,
			"a natural operation lists its identifiers in x-idempotency-natural",
		)
		return
	}
	for _, e := range entries {
		if e == "route" {
			if len(entries) != 1 {
				report(
					ruleNaturalIdentifier,
					"route is the only entry when it is used, not one of %v",
					entries,
				)
			}
			continue
		}
		in, name, ok := strings.Cut(e, ":")
		switch {
		case !ok:
			report(ruleNaturalIdentifier, "%q is not <in>:<name> or route", e)
		case in == "body":
			// A JSON Pointer (RFC 6901), whose empty form is the whole body,
			// or archive, the coordinates an uploaded package declares.
			hasBody := o.op.RequestBody != nil && o.op.RequestBody.Value != nil
			if !hasBody || name != "" && name != "archive" && !strings.HasPrefix(name, "/") {
				report(
					ruleNaturalIdentifier,
					"%q names no request body element: a JSON Pointer such as body:/name, or body:archive",
					e,
				)
			}
		case name == "":
			report(ruleNaturalIdentifier, "%q is not <in>:<name> or route", e)
		case in == openapi3.ParameterInHeader && undeclarableHeader(name):
			// OpenAPI ignores a declared Accept, Content-Type or
			// Authorization parameter, so the request names it all the same
			// (a multipart upload's Bearer token) without a declaration.
		case in == openapi3.ParameterInPath || in == openapi3.ParameterInQuery ||
			in == openapi3.ParameterInHeader:
			if !o.hasParameter(in, name) {
				report(ruleNaturalIdentifier, "%q names no %s parameter of the operation", e, in)
			}
		default:
			report(ruleNaturalIdentifier, "%q: the element is in path, query, header or body", e)
		}
	}
}

// undeclarableHeader reports the headers OpenAPI 3.0 forbids an operation
// to declare as parameters.
func undeclarableHeader(name string) bool {
	for _, h := range []string{"Accept", "Content-Type", "Authorization"} {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// checkCost holds x-cost and x-cost-expensive-query to the admission the
// boundary gives the operation's method and path today, which is what
// RATE-01 will read from the spec instead, so a difference would change an
// answer. It returns the declared class.
func checkCost(o principleOp, report reportFunc) string {
	cost := classValue(o, "x-cost", costClasses, true, report)
	raw, hasQuery := o.op.Extensions["x-cost-expensive-query"]
	if cost == "" {
		return cost
	}
	method, path := strings.ToUpper(o.method), o.examplePath()
	if want := admissionClass(method, path, nil); want != cost {
		report(
			ruleCostAdmission,
			"x-cost is %s, but %s %s is admitted as %s",
			cost,
			method,
			path,
			want,
		)
		return cost
	}
	declared, ok := stringList(raw)
	switch {
	case hasQuery && cost != "ordinary":
		report(
			ruleCostAdmission,
			"x-cost-expensive-query belongs only to an ordinary operation, not %s",
			cost,
		)
		return cost
	case hasQuery && !ok:
		report(
			ruleCostAdmission,
			"x-cost-expensive-query is a list of query parameter names, not %v",
			raw,
		)
		return cost
	}
	for _, q := range declared {
		if !o.hasParameter(openapi3.ParameterInQuery, q) {
			report(
				ruleCostAdmission,
				"x-cost-expensive-query names %q, which is no query parameter of the operation",
				q,
			)
		} else if admissionClass(method, path, url.Values{q: {""}}) != "expensive" {
			report(
				ruleCostAdmission,
				"x-cost-expensive-query names %q, but %s %s?%s is not admitted as expensive",
				q,
				method,
				path,
				q,
			)
		}
	}
	if cost != "ordinary" {
		return cost
	}
	for _, p := range o.parameters() {
		if p.Value == nil || p.Value.In != openapi3.ParameterInQuery ||
			slices.Contains(declared, p.Value.Name) {
			continue
		}
		if admissionClass(method, path, url.Values{p.Value.Name: {""}}) == "expensive" {
			report(
				ruleCostAdmission,
				"%s %s?%s is admitted as expensive, but x-cost-expensive-query lacks %s",
				method,
				path,
				p.Value.Name,
				p.Value.Name,
			)
		}
	}
	return cost
}

// checkAdmissionResponses holds the 429 and 503 to what admission writes:
// every admitted operation shows both, with their headers declared by the
// shared components; an operation whose path admission answers in another
// envelope than its spec's ErrorResponse declares its own; and the rate
// headers appear on no other response. An operation's own 503 on its
// spec's envelope also reports a condition of its own, which may not be a
// hint to retry (decision 0043, "Rate headers and Retry-After"), so it is
// left as it stands.
func checkAdmissionResponses(o principleOp, cost string, otherEnvelope bool, report reportFunc) {
	shared := map[string]string{"429": sharedTooManyRequests, "503": sharedServiceUnavailable}
	statuses := []string{"429", "503"}
	for _, status := range statuses {
		resp := o.op.Responses.Value(status)
		ref := ""
		if resp != nil {
			ref = resp.Ref
		}
		switch {
		case cost == "exempt":
			if ref == shared[status] {
				report(
					ruleAdmissionResponses,
					"an exempt operation is never admitted, so it does not reference %s",
					ref,
				)
			}
			continue
		case resp == nil:
			report(ruleAdmissionResponses, "an admitted operation declares %s", status)
			continue
		case ref == shared[status]:
			if otherEnvelope {
				report(
					ruleAdmissionEnvelope,
					"admission answers this path in another envelope than ErrorResponse, so its %s is its own, not %s",
					status,
					ref,
				)
			}
			continue
		}
		if status == "503" && !otherEnvelope {
			continue
		}
		for header, component := range sortedHeaders() {
			h := resp.Value.Headers[header]
			if h == nil || h.Ref != component {
				report(
					ruleAdmissionHeaders,
					"its own %s declares %s by referencing %s",
					status,
					header,
					component,
				)
			}
		}
	}
	codes := make([]string, 0, o.op.Responses.Len())
	for status := range o.op.Responses.Map() {
		codes = append(codes, status)
	}
	sort.Strings(codes)
	for _, status := range codes {
		resp := o.op.Responses.Value(status)
		if resp.Value == nil {
			continue
		}
		for name, h := range resp.Value.Headers {
			component, rate := canonicalRateHeader(name)
			switch {
			case !rate:
			case h.Ref != component:
				report(
					ruleAdmissionHeaders,
					"%s %s is declared inline rather than referencing %s",
					status,
					name,
					component,
				)
			case name != "Retry-After" && !slices.Contains(statuses, status):
				report(
					ruleAdmissionHeaders,
					"%s declares %s, which only 429 and 503 declare",
					status,
					name,
				)
			}
		}
	}
}

// sortedHeaders yields the shared rate headers in a stable order, so a
// violation's detail does not change between runs.
func sortedHeaders() func(func(string, string) bool) {
	return func(yield func(string, string) bool) {
		names := make([]string, 0, len(sharedRateHeaders))
		for n := range sharedRateHeaders {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !yield(n, sharedRateHeaders[n]) {
				return
			}
		}
	}
}

func canonicalRateHeader(name string) (string, bool) {
	for n, component := range sharedRateHeaders {
		if strings.EqualFold(n, name) {
			return component, true
		}
	}
	return "", false
}

// checkPagination holds x-pagination and its companions to decision 0043's
// table, in the shape every lane writes them: x-pagination-parameters a map
// of position and size, the one missing left out; x-pagination-next on
// every collection, none on complete. It also requires x-pagination of an
// operation the guard can tell is a collection (collectionEvidence).
func checkPagination(o principleOp, report reportFunc) {
	_, has := o.op.Extensions["x-pagination"]
	class := classValue(o, "x-pagination", paginationClasses, false, report)
	params, hasParams := o.op.Extensions["x-pagination-parameters"]
	next, hasNext := o.op.Extensions["x-pagination-next"]
	if !has {
		if hasParams || hasNext {
			report(
				rulePaginationDeclaration,
				"x-pagination-parameters and x-pagination-next need x-pagination",
			)
		}
		if why := collectionEvidence(o); why != "" {
			report(ruleCollectionPagination, "x-pagination is missing on a collection: %s", why)
		}
		return
	}
	if class == "" {
		return
	}
	paged := class == "cursor" || class == "offset" || class == "page"
	switch m, ok := params.(map[string]any); {
	case !hasParams:
		if paged {
			report(
				rulePaginationDeclaration,
				"a %s operation names its position in x-pagination-parameters",
				class,
			)
		}
	case !ok:
		report(rulePaginationDeclaration,
			"x-pagination-parameters maps position and size to parameter names, not %v", params)
	case class == "complete":
		report(rulePaginationDeclaration, "a complete operation has no x-pagination-parameters")
	default:
		if _, ok := m["position"]; paged && !ok {
			report(
				rulePaginationDeclaration,
				"a %s operation names its position in x-pagination-parameters",
				class,
			)
		}
		for _, k := range sortedKeys(m) {
			name, _ := m[k].(string)
			switch {
			case k != "position" && k != "size":
				report(
					rulePaginationDeclaration,
					"x-pagination-parameters holds only position and size, not %s",
					k,
				)
			case !o.hasAnyParameter(name):
				report(
					rulePaginationDeclaration,
					"x-pagination-parameters %s names %v, which is no parameter of the operation",
					k,
					m[k],
				)
			}
		}
	}
	n, _ := next.(string)
	pointer := n == "link" || strings.HasPrefix(n, "body:/")
	switch {
	case class == "cursor" && !pointer:
		report(
			ruleCursorNext,
			"a cursor operation names its next pointer in x-pagination-next (link or body:<JSON Pointer>), not %v",
			next,
		)
	case paged && !pointer:
		report(
			rulePaginationDeclaration,
			"an %s operation names its next pointer in x-pagination-next (link or body:<JSON Pointer>), not %v",
			class,
			next,
		)
	case !hasNext:
		report(
			rulePaginationDeclaration,
			"a %s operation declares x-pagination-next, none when it has no pointer",
			class,
		)
	case class == "complete" && n != "none":
		report(
			rulePaginationDeclaration,
			"a complete operation has no next pointer: x-pagination-next is none, not %v",
			next,
		)
	case !pointer && n != "none":
		report(
			rulePaginationDeclaration,
			"x-pagination-next is link, body:<JSON Pointer> or none, not %v",
			next,
		)
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collectionEvidence says why the operation is a collection by a rule the
// guard can check, or returns "" when the guard cannot tell. Decision 0043
// counts a top-level array and the item array of an envelope, and not an
// array inside one resource or an action's report, which no schema
// distinguishes from an envelope's. So the guard holds only:
//   - a read whose success body is a top-level array is a collection;
//   - a read that takes a paging parameter (pageControl) and answers an
//     array anywhere in its success body is a collection.
//
// A read is x-idempotency safe, or, before it is classified, a GET. Every
// other envelope is the author's call: declaring complete on one the guard
// cannot see an array in (a free-form body) is accepted.
func collectionEvidence(o principleOp) string {
	switch v, _ := o.op.Extensions["x-idempotency"].(string); {
	case v == "" && o.method != http.MethodGet, v != "" && v != "safe":
		return ""
	}
	var arrays []string
	for _, status := range sortedResponseKeys(o.op.Responses) {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		resp := o.op.Responses.Value(status)
		if resp.Value == nil {
			continue
		}
		for _, media := range sortedMedia(resp.Value.Content) {
			mt := resp.Value.Content[media]
			if mt.Schema == nil || mt.Schema.Value == nil {
				continue
			}
			if mt.Schema.Value.Type.Is(openapi3.TypeArray) {
				return status + " " + media + " is an array"
			}
			apiReviewCollections(
				status+" "+media,
				mt.Schema.Value,
				map[*openapi3.Schema]bool{},
				&arrays,
			)
		}
	}
	if len(arrays) == 0 {
		return ""
	}
	for _, p := range o.parameters() {
		if p.Value != nil && p.Value.In == openapi3.ParameterInQuery && pageControl(p.Value.Name) {
			return "it takes the paging parameter " + p.Value.Name + " and answers " + arrays[0]
		}
	}
	return ""
}

func sortedResponseKeys(r *openapi3.Responses) []string {
	out := make([]string, 0, r.Len())
	for k := range r.Map() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMedia(c openapi3.Content) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// principleSummary groups violations by rule and spec, with the count and
// the first examples of each, so a failure over hundreds of operations
// reads as a short report.
func principleSummary(violations []principleViolation, examples int) string {
	type group struct {
		count int
		first []string
	}
	byRule := map[string]map[string]*group{}
	for _, v := range violations {
		if byRule[v.Rule] == nil {
			byRule[v.Rule] = map[string]*group{}
		}
		g := byRule[v.Rule][v.Spec]
		if g == nil {
			g = &group{}
			byRule[v.Rule][v.Spec] = g
		}
		g.count++
		if len(g.first) < examples {
			g.first = append(g.first, v.Op+": "+v.Detail)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d violations of decision 0043's operation rules\n", len(violations))
	rules := make([]string, 0, len(byRule))
	for r := range byRule {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	for _, r := range rules {
		total := 0
		specs := make([]string, 0, len(byRule[r]))
		for s, g := range byRule[r] {
			specs = append(specs, s)
			total += g.count
		}
		sort.Strings(specs)
		fmt.Fprintf(&b, "\nrule %s: %d\n", r, total)
		for _, s := range specs {
			g := byRule[r][s]
			fmt.Fprintf(&b, "  %s: %d\n", s, g.count)
			for _, e := range g.first {
				fmt.Fprintf(&b, "    %s\n", e)
			}
		}
	}
	return b.String()
}

// routerServes reports whether the router serves an operation rather than
// a 501 stub, as the API reliability ledger reads it.
func routerServes(t *testing.T) servedFunc {
	t.Helper()
	stubbed := stubbedHandwritten(t)
	return func(route string, op *openapi3.Operation) bool {
		handwritten, _ := op.Extensions["x-handwritten"].(bool)
		return implemented[route] || handwritten && !stubbed[route]
	}
}

// TestAccAPIPrinciples holds every operation of the seven bundled specs to
// decision 0043's rules: its idempotency, cost and paging classes, the 429
// and 503 admission answers with their headers, and the key parameter, which
// every served key operation declares and no stub does. A
// failure prints one grouped summary: per rule and spec, the count and the
// first examples.
func TestAccAPIPrinciples(t *testing.T) {
	served := routerServes(t)
	var all []principleViolation
	operations := 0
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		for _, item := range doc.Paths.Map() {
			operations += len(item.Operations())
		}
		all = append(
			all,
			checkAPIPrinciples(strings.TrimSuffix(name, ".openapi.yaml"), doc, served)...)
	}
	if operations == 0 {
		t.Fatal("no operations checked")
	}
	if len(all) > 0 {
		t.Errorf("checked %d operations\n%s", operations, principleSummary(all, 3))
	}
}

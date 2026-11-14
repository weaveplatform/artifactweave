package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// principleRules is every rule of api_principles_test.go; each needs a
// fixture below that breaks it, so a new rule is added here and given one.
var principleRules = []string{
	ruleClassMissing, ruleClassTodo, ruleClassValue, ruleNaturalIdentifier, ruleNoneReason,
	ruleKeyParameter, ruleKeyServed, ruleKeyStub, ruleCostAdmission, ruleAdmissionResponses,
	ruleAdmissionHeaders,
	ruleAdmissionEnvelope, ruleCollectionPagination, ruleCursorNext, rulePaginationDeclaration,
}

// The answers every admitted fixture operation declares, and its own 429
// and 503 for a path admission answers in another envelope.
const (
	admitted = `
    responses:
      '200': {description: ok}
      '429': {$ref: '#/components/responses/TooManyRequests'}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}`
	ownRateHeaders = `
          headers:
            Retry-After: {$ref: '#/components/headers/RetryAfter'}
            RateLimit: {$ref: '#/components/headers/RateLimit'}
            RateLimit-Policy: {$ref: '#/components/headers/RateLimitPolicy'}`
	repoKey = `
    parameters:
      - {name: repoKey, in: path, required: true, schema: {type: string}}`
)

// validPaths breaks no rule: one operation of each class and shape the
// guard accepts. x-tier: stub marks a fixture operation a 501 stub serves
// (fixtureServed).
var validPaths = `
/api/system/ping:
  get:
    operationId: ping
    x-idempotency: safe
    x-cost: exempt
    responses:
      '200': {description: ok}
/api/artifactweave/api-restrictions/{user}:
  get:
    operationId: getRestriction
    x-idempotency: safe
    x-cost: recovery
    parameters:
      - {name: user, in: path, required: true, schema: {type: string}}` + admitted + `
/api/repositories/{repoKey}:
  get:
    operationId: getRepository
    x-idempotency: safe
    x-cost: ordinary` + repoKey + admitted + `
  put:
    operationId: createRepository
    x-idempotency: natural
    x-idempotency-natural: ['path:repoKey', 'body:/rclass', 'header:Content-Type']
    x-cost: expensive
    requestBody: {content: {application/json: {schema: {type: object}}}}` + repoKey + admitted + `
  delete:
    operationId: deleteRepository
    x-idempotency: natural
    x-idempotency-natural: [route]
    x-cost: expensive` + repoKey + admitted + `
/api/build/append/{buildName}:
  post:
    operationId: appendBuild
    x-idempotency: key
    x-cost: expensive
    parameters:
      - {name: buildName, in: path, required: true, schema: {type: string}}
      - $ref: '#/components/parameters/IdempotencyKey'` + admitted + `
/api/build/promote/{buildName}:
  post:
    operationId: promoteStub
    x-tier: stub
    x-idempotency: key
    x-cost: expensive
    parameters:
      - {name: buildName, in: path, required: true, schema: {type: string}}` + admitted + `
/api/security/token:
  post:
    operationId: createToken
    x-idempotency: none
    x-idempotency-reason: secret-response
    x-cost: expensive` + admitted + `
/api/storage/{repoKey}:
  get:
    operationId: listFolder
    x-idempotency: safe
    x-cost: ordinary
    x-cost-expensive-query: [list]
    x-pagination: complete
    x-pagination-next: none
    parameters:
      - {name: repoKey, in: path, required: true, schema: {type: string}}
      - {name: list, in: query, schema: {type: string}}
    responses:
      '200': {description: ok, content: {application/json: {schema: {type: array, items: {type: string}}}}}
      '429': {$ref: '#/components/responses/TooManyRequests'}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}
/api/search/offset:
  get:
    operationId: searchOffset
    x-idempotency: safe
    x-cost: expensive
    x-pagination: offset
    x-pagination-parameters: {position: offset, size: limit}
    x-pagination-next: body:/next
    parameters:
      - {name: offset, in: query, schema: {type: integer}}
      - {name: limit, in: query, schema: {type: integer}}` + admitted + `
/api/search/cursor:
  get:
    operationId: searchCursor
    x-idempotency: safe
    x-cost: expensive
    x-pagination: cursor
    x-pagination-parameters: {position: cursor}
    x-pagination-next: link
    parameters:
      - {name: cursor, in: query, schema: {type: string}}` + admitted + `
/api/go/{repoKey}/@v/list:
  get:
    operationId: goList
    x-idempotency: safe
    x-cost: ordinary` + repoKey + `
    responses:
      '200': {description: ok}
      '429':
          description: own` + ownRateHeaders + `
      '503':
          description: own` + ownRateHeaders + `
/api/scaffolded:
  get:
    operationId: scaffolded
    x-draft: true
    x-idempotency: TODO(author)
    x-cost: TODO(author)` + admitted + `
`

// principleFixtures are paths that each break the rules listed with them,
// and only those.
var principleFixtures = []struct {
	name, paths string
	want        []string
}{
	{"valid", validPaths, nil},
	{
		"class missing", `
/api/repositories:
  get:
    operationId: listRepositories
    x-cost: expensive
    x-pagination: complete
    x-pagination-next: none` + admitted,
		[]string{ruleClassMissing},
	},
	{
		"placeholder without x-draft", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: TODO(author)` + admitted,
		[]string{ruleClassTodo},
	},
	{
		"unknown class value", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: everything` + admitted,
		[]string{ruleClassValue},
	},
	{
		"natural identifier the operation lacks", `
/api/x:
  put:
    operationId: x
    x-idempotency: natural
    x-idempotency-natural: ['query:name', 'body:/a', route, 'cookie:c', 'path', 'path:']
    x-cost: expensive` + admitted,
		[]string{ruleNaturalIdentifier},
	},
	{
		"natural identifier on another class", `
/api/x:
  put:
    operationId: x
    x-tier: stub
    x-idempotency: key
    x-idempotency-natural: [route]
    x-cost: expensive` + admitted,
		[]string{ruleNaturalIdentifier},
	},
	{
		"none without a known reason", `
/api/x:
  post:
    operationId: x
    x-idempotency: none
    x-idempotency-reason: because
    x-cost: expensive` + admitted,
		[]string{ruleNoneReason},
	},
	{
		"reason on another class", `
/api/x:
  post:
    operationId: x
    x-idempotency: natural
    x-idempotency-natural: [route]
    x-idempotency-reason: secret-response
    x-cost: expensive` + admitted,
		[]string{ruleNoneReason},
	},
	{
		"key declared inline and on a natural operation", `
/api/x:
  post:
    operationId: x
    x-idempotency: natural
    x-idempotency-natural: [route]
    x-cost: expensive
    parameters:
      - {name: Idempotency-Key, in: header, schema: {type: string}}` + admitted,
		[]string{ruleKeyParameter},
	},
	{
		"served key operation without the key", `
/api/build/promote/{buildName}:
  post:
    operationId: promoteBuild
    x-idempotency: key
    x-cost: expensive
    parameters:
      - {name: buildName, in: path, required: true, schema: {type: string}}` + admitted,
		[]string{ruleKeyServed},
	},
	{
		"stub with the key", `
/api/build/promote/{buildName}:
  post:
    operationId: promoteBuild
    x-tier: stub
    x-idempotency: key
    x-cost: expensive
    parameters:
      - {name: buildName, in: path, required: true, schema: {type: string}}
      - $ref: '#/components/parameters/IdempotencyKey'` + admitted,
		[]string{ruleKeyStub},
	},
	{
		"cost other than admission's", `
/api/search/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary` + admitted,
		[]string{ruleCostAdmission},
	},
	{
		"expensive query undeclared, unknown and not expensive", `
/api/storage/{repoKey}:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    x-cost-expensive-query: [missing, depth]
    parameters:
      - {name: repoKey, in: path, required: true, schema: {type: string}}
      - {name: list, in: query, schema: {type: string}}
      - {name: depth, in: query, schema: {type: string}}` + admitted,
		[]string{ruleCostAdmission},
	},
	{
		"expensive query on an expensive operation", `
/api/search/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: expensive
    x-cost-expensive-query: [list]` + admitted,
		[]string{ruleCostAdmission},
	},
	{
		"admitted without 429, exempt with 503", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    responses:
      '200': {description: ok}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}
/api/system/ping:
  get:
    operationId: ping
    x-idempotency: safe
    x-cost: exempt` + admitted,
		[]string{ruleAdmissionResponses},
	},
	{
		"own 429 with inline and missing headers, rate header on 200", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    responses:
      '200':
        description: ok
        headers:
          RateLimit: {$ref: '#/components/headers/RateLimit'}
      '429':
        description: own
        headers:
          Retry-After: {schema: {type: integer}}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}`,
		[]string{ruleAdmissionHeaders},
	},
	{
		"shared answers on another envelope", `
/api/go/{repoKey}/@v/list:
  get:
    operationId: goList
    x-idempotency: safe
    x-cost: ordinary` + repoKey + admitted,
		[]string{ruleAdmissionEnvelope},
	},
	{
		"array read without x-pagination", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    responses:
      '200': {description: ok, content: {application/json: {schema: {type: array, items: {type: string}}}}}
      '429': {$ref: '#/components/responses/TooManyRequests'}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}
/api/y:
  get:
    operationId: y
    x-idempotency: safe
    x-cost: ordinary
    parameters:
      - {name: page, in: query, schema: {type: integer}}
    responses:
      '200':
        description: ok
        content:
          application/json:
            schema: {type: object, properties: {results: {type: array, items: {type: string}}}}
      '429': {$ref: '#/components/responses/TooManyRequests'}
      '503': {$ref: '#/components/responses/ServiceUnavailable'}`,
		[]string{ruleCollectionPagination},
	},
	{
		"cursor without a next pointer", `
/api/x:
  get:
    operationId: x
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: cursor
    x-pagination-parameters: {position: cursor}
    x-pagination-next: none
    parameters:
      - {name: cursor, in: query, schema: {type: string}}` + admitted,
		[]string{ruleCursorNext},
	},
	{
		"pagination companions that do not fit", `
/api/a:
  get:
    operationId: a
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: complete
    x-pagination-parameters: {size: limit}
    x-pagination-next: link
    parameters:
      - {name: limit, in: query, schema: {type: integer}}` + admitted + `
/api/b:
  get:
    operationId: b
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: page
    x-pagination-parameters: {size: pageSize, order: sort}
    x-pagination-next: none` + admitted + `
/api/c:
  get:
    operationId: c
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: native` + admitted + `
/api/d:
  get:
    operationId: d
    x-idempotency: safe
    x-cost: ordinary
    x-pagination-next: link` + admitted + `
/api/e:
  get:
    operationId: e
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: offset
    x-pagination-parameters: offset
    x-pagination-next: body:/next` + admitted + `
/api/f:
  get:
    operationId: f
    x-idempotency: safe
    x-cost: ordinary
    x-pagination: native
    x-pagination-next: elsewhere` + admitted,
		[]string{rulePaginationDeclaration},
	},
}

// fixtureServed serves every fixture operation that x-tier: stub does not
// mark a stub.
func fixtureServed(_ string, op *openapi3.Operation) bool {
	tier, _ := op.Extensions["x-tier"].(string)
	return tier != "stub"
}

// principleFixtureDoc is a spec served under /artifactory with paths and
// the real shared components, so a fixture meets the declarations the
// bundles reference.
func principleFixtureDoc(t *testing.T, paths string) *openapi3.T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repopath.Root(t), "api", "src", "components-shared.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Components map[string]any `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &shared); err != nil {
		t.Fatal(err)
	}
	shared.Components["schemas"] = map[string]any{"ErrorResponse": map[string]any{"type": "object"}}
	var p map[string]any
	if err := yaml.Unmarshal([]byte(paths), &p); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"openapi":    "3.0.3",
		"info":       map[string]any{"title": "fixture", "version": "1"},
		"servers":    []any{map[string]any{"url": "/artifactory"}},
		"paths":      p,
		"components": shared.Components,
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestUnitAPIPrinciplesFixtures(t *testing.T) {
	broken := map[string]bool{}
	for _, f := range principleFixtures {
		t.Run(f.name, func(t *testing.T) {
			violations := checkAPIPrinciples(
				"fixture",
				principleFixtureDoc(t, f.paths),
				fixtureServed,
			)
			var got []string
			for _, v := range violations {
				got = append(got, v.Rule)
			}
			slices.Sort(got)
			got = slices.Compact(got)
			if !slices.Equal(got, f.want) {
				t.Errorf("rules %v, want %v\n%s", got, f.want, principleSummary(violations, 10))
			}
			for _, r := range got {
				broken[r] = true
			}
		})
	}
	for _, r := range principleRules {
		if !broken[r] {
			t.Errorf("no fixture breaks rule %s", r)
		}
	}
}

func TestUnitPrincipleSummary(t *testing.T) {
	got := principleSummary([]principleViolation{
		{Rule: ruleClassMissing, Spec: "b", Op: "GET /b", Detail: "x-cost is missing"},
		{Rule: ruleClassMissing, Spec: "a", Op: "GET /a1", Detail: "x-cost is missing"},
		{Rule: ruleClassMissing, Spec: "a", Op: "GET /a2", Detail: "x-cost is missing"},
		{Rule: ruleCursorNext, Spec: "a", Op: "GET /c", Detail: "none"},
	}, 1)
	want := strings.Join([]string{
		"4 violations of decision 0043's operation rules",
		"",
		"rule class-missing: 3",
		"  a: 2",
		"    GET /a1: x-cost is missing",
		"  b: 1",
		"    GET /b: x-cost is missing",
		"",
		"rule cursor-next: 1",
		"  a: 1",
		"    GET /c: none",
		"",
	}, "\n")
	if got != want {
		t.Errorf("summary:\n%s\nwant:\n%s", got, want)
	}
}

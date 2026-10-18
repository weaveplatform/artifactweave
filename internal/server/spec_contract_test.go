package server

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// codeSpan matches a Markdown code span. Identifiers and literal values such
// as `X-JFrog-Download-Redirect-To`, `/artifactory/api/...` or a licence type
// of `artifactweave` are part of the contract and may name a product; prose
// outside them may not.
var codeSpan = regexp.MustCompile("`[^`]*`")

// commentary reports why a description is not a statement of the served
// contract (docs/implementation/api-description-conventions.md, #432), or ""
// when it is. Comparisons with Artifactory, release history and the reasons
// behind a choice live in the research, the decision records and the
// deviations register; generated Go comments and clients inherit whatever
// the spec says, so the check runs on every description in the bundle.
func commentary(text string) string {
	prose := codeSpan.ReplaceAllString(text, "")
	if strings.Contains(strings.ToLower(prose), "artifactweave") {
		return "names artifactweave outside a code span"
	}
	for _, name := range []string{"Artifactory", "JFrog"} {
		if strings.Contains(prose, name) {
			return "names " + name + " outside a code span"
		}
	}
	return ""
}

func TestDescriptionCommentary(t *testing.T) {
	for _, tc := range []struct {
		text string
		want string
	}{
		{"Optional within this object. The repository key.", ""},
		{"Echoes `X-JFrog-Download-Redirect-To` when set.", ""},
		{"Served under `/artifactory/api/storage`.", ""},
		{"Licence type; always `artifactweave`.", ""},
		{"Never returned by artifactweave; documented for parity.", "names artifactweave outside a code span"},
		{"ArtifactWeave stores nothing.", "names artifactweave outside a code span"},
		{"Artifactory lists one licence per node.", "names Artifactory outside a code span"},
		{"JFrog documents an array.", "names JFrog outside a code span"},
		{"Real `Artifactory` servers; Artifactory answers 206.", "names Artifactory outside a code span"},
	} {
		if got := commentary(tc.text); got != tc.want {
			t.Errorf("commentary(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// TestOpenAPIDescriptionsDescribeContract holds every description and
// summary in the bundled specs to the served contract: no product
// comparison, no history, no rationale (#432).
func TestOpenAPIDescriptionsDescribeContract(t *testing.T) {
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		var problems []string
		check := func(where, text string) {
			if why := commentary(text); why != "" {
				problems = append(problems, where+": "+why)
			}
		}
		seen := map[*openapi3.Schema]bool{}
		if doc.Info != nil {
			check(name+" info.title", doc.Info.Title)
			check(name+" info.description", doc.Info.Description)
		}
		for schemaName, ref := range doc.Components.Schemas {
			walkSchemaText(name+"#"+schemaName, ref.Value, seen, check)
		}
		for path, item := range doc.Paths.Map() {
			check(name+" "+path+" summary", item.Summary)
			check(name+" "+path+" description", item.Description)
			for method, op := range item.Operations() {
				where := name + " " + method + " " + path
				check(where+" summary", op.Summary)
				check(where+" description", op.Description)
				for _, p := range op.Parameters {
					check(where+" param "+p.Value.Name, p.Value.Description)
					if p.Value.Schema != nil {
						walkSchemaText(where+" param "+p.Value.Name, p.Value.Schema.Value, seen, check)
					}
				}
				if rb := op.RequestBody; rb != nil && rb.Value != nil {
					check(where+" request", rb.Value.Description)
					for _, mt := range rb.Value.Content {
						if mt.Schema != nil {
							walkSchemaText(where+" request", mt.Schema.Value, seen, check)
						}
					}
				}
				for status, resp := range op.Responses.Map() {
					if resp.Value == nil {
						continue
					}
					if resp.Value.Description != nil {
						check(where+" response "+status, *resp.Value.Description)
					}
					for header, h := range resp.Value.Headers {
						if h.Value != nil {
							check(where+" response "+status+" header "+header, h.Value.Description)
						}
					}
					for _, mt := range resp.Value.Content {
						if mt.Schema != nil {
							walkSchemaText(where+" response "+status, mt.Schema.Value, seen, check)
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

// walkSchemaText visits the description of s and of every schema reachable
// from it once.
func walkSchemaText(
	where string,
	s *openapi3.Schema,
	seen map[*openapi3.Schema]bool,
	check func(where, text string),
) {
	if s == nil || seen[s] {
		return
	}
	seen[s] = true
	check(where, s.Description)
	for prop, ref := range s.Properties {
		walkSchemaText(where+"."+prop, ref.Value, seen, check)
	}
	if s.Items != nil {
		walkSchemaText(where+"[]", s.Items.Value, seen, check)
	}
	if ap := s.AdditionalProperties.Schema; ap != nil {
		walkSchemaText(where+"{}", ap.Value, seen, check)
	}
	for _, group := range []openapi3.SchemaRefs{s.AllOf, s.OneOf, s.AnyOf} {
		for _, sub := range group {
			walkSchemaText(where, sub.Value, seen, check)
		}
	}
}

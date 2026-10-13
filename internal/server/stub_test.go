package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// implemented lists the generated operations that have real handlers. Every
// other generated operation must reach the UnimplementedStrict stub and answer
// 501 in its service's envelope; this list grows as phases land.
var implemented = map[string]bool{
	"GET /artifactory/api/system/version":      true,
	"GET /artifactory/api/system/license":      true,
	"GET /artifactory/api/v1/system/readiness": true,
	"GET /artifactory/api/v1/system/liveness":  true,
	"GET /access/api/v1/system/version":        true,

	"GET /artifactory/api/repositories":                                true,
	"GET /artifactory/api/repositories/configurations":                 true,
	"GET /artifactory/api/repositories/existence":                      true,
	"GET /artifactory/api/repositories/{remoteRepoName}/configuration": true,
	"GET /artifactory/api/repositories/{repoKey}":                      true,
	"PUT /artifactory/api/repositories/{repoKey}":                      true,
	"POST /artifactory/api/repositories/{repoKey}":                     true,
	"DELETE /artifactory/api/repositories/{repoKey}":                   true,
	"GET /artifactory/api/v2/repositories/{repoKey}":                   true,
	"GET /artifactory/api/v2/repositories/batch":                       true,
	"PUT /artifactory/api/v2/repositories/batch":                       true,
	"POST /artifactory/api/v2/repositories/batch":                      true,
	"DELETE /artifactory/api/v2/repositories/batch":                    true,
}

// TestUnimplementedOperationsAnswer501 proves the generated stubs reach the
// client: a specified but unimplemented operation answers 501 rather than 404
// (which would tell clients the endpoint does not exist) or 500.
func TestUnimplementedOperationsAnswer501(t *testing.T) {
	h := testHandler()
	checked := 0
	for _, name := range specFiles {
		doc := loadSpec(t, name)
		prefix := doc.Servers[0].URL
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				key := method + " " + prefix + path
				if implemented[key] {
					continue
				}
				if v, ok := op.Extensions["x-handwritten"]; ok && v == true {
					continue
				}
				params := append(openapi3.Parameters{}, item.Parameters...)
				params = append(params, op.Parameters...)
				url := prefix + fillParams(path)
				var query []string
				for _, p := range params {
					if p.Value.Required && p.Value.In == "query" {
						query = append(query, p.Value.Name+"="+plausibleValue(p.Value))
					}
				}
				if len(query) > 0 {
					url += "?" + strings.Join(query, "&")
				}
				body, contentType := plausibleBody(op.RequestBody)
				req := httptest.NewRequest(method, url, body)
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				for _, p := range params {
					if p.Value.Required && p.Value.In == "header" {
						req.Header.Set(p.Value.Name, plausibleValue(p.Value))
					}
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				checked++
				if rec.Code != http.StatusNotImplemented {
					t.Errorf("%s: status %d, body %s", key, rec.Code, rec.Body.String())
					continue
				}
				// The body must be exactly what the envelope table renders for
				// this path, so each product keeps its documented error shape.
				want := httptest.NewRecorder()
				apierror.Write(want, surfaceFor(req.URL.Path), apierror.ErrNotImplemented)
				if !sameBody(want.Body.Bytes(), rec.Body.Bytes()) {
					t.Errorf("%s: body %s, want %s", key, rec.Body.String(), want.Body.String())
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d unimplemented operations checked; the specs look truncated", checked)
	}
}

// fillParams substitutes a plausible value for every path parameter so the
// request passes the generated binder; the stub reads none of them.
func fillParams(path string) string {
	var b strings.Builder
	for {
		i := strings.Index(path, "{")
		if i < 0 {
			b.WriteString(path)
			return b.String()
		}
		j := strings.Index(path[i:], "}")
		b.WriteString(path[:i])
		b.WriteString("1")
		path = path[i+j+1:]
	}
}

// plausibleValue satisfies the generated binder for a parameter's type.
func plausibleValue(p *openapi3.Parameter) string {
	if p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Type != nil {
		switch {
		case p.Schema.Value.Type.Is("integer"), p.Schema.Value.Type.Is("number"):
			return "1"
		case p.Schema.Value.Type.Is("boolean"):
			return "true"
		}
		if len(p.Schema.Value.Enum) > 0 {
			if s, ok := p.Schema.Value.Enum[0].(string); ok {
				return s
			}
		}
	}
	return "x"
}

// plausibleBody returns a body the generated binder accepts for the first
// declared media type: an empty JSON object or array, or a short text body.
func plausibleBody(rb *openapi3.RequestBodyRef) (*strings.Reader, string) {
	if rb == nil || rb.Value == nil {
		return strings.NewReader(""), ""
	}
	types := make([]string, 0, len(rb.Value.Content))
	for mt := range rb.Value.Content {
		types = append(types, mt)
	}
	sort.Strings(types)
	for _, mt := range types {
		if mt == "application/json" || strings.HasSuffix(mt, "+json") {
			schema := rb.Value.Content[mt].Schema
			if schema != nil && schema.Value != nil && schema.Value.Type != nil &&
				schema.Value.Type.Is("array") {
				return strings.NewReader("[]"), mt
			}
			return strings.NewReader("{}"), mt
		}
	}
	if len(types) == 0 {
		return strings.NewReader(""), ""
	}
	return strings.NewReader("x"), types[0]
}

// sameBody compares JSON bodies structurally and anything else byte for byte.
func sameBody(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}

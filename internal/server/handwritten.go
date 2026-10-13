package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/api"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

// registerHandwrittenStubs gives every spec operation marked x-handwritten a
// 501 route unless a real handler already claimed the same method and
// pattern. Generated operations get the same treatment from stubgen; without
// this, a hand-written operation that is specified but not yet implemented
// would fall through to the 404 catch-all and look like an unknown path to
// clients, which is the wrong signal (research 10).
func registerHandwrittenStubs(r chi.Router, logger *slog.Logger) {
	existing := registeredRoutes(r)
	for _, name := range api.SpecFiles {
		doc, err := loadEmbeddedSpec(name)
		if err != nil {
			// The bundled documents are validated by make generate and by the
			// spec tests; failing here would only hide a build problem.
			panic(fmt.Sprintf("server: embedded spec %s: %v", name, err))
		}
		prefix := ""
		if len(doc.Servers) > 0 {
			prefix = doc.Servers[0].URL
		}
		for path, item := range doc.Paths.Map() {
			for method, op := range item.Operations() {
				if v, ok := op.Extensions["x-handwritten"]; !ok || v != true {
					continue
				}
				// Documentation aliases (JFrog pages that describe a header
				// variant under a path no client sends) stay in the spec for
				// parity but must not shadow real item paths.
				if v, ok := op.Extensions["x-not-routed"]; ok && v == true {
					continue
				}
				for _, pattern := range chiPatterns(prefix + path) {
					key := method + " " + pattern
					if existing[key] {
						continue
					}
					existing[key] = true
					r.Method(method, pattern, handwrittenStub(method, prefix+path))
					logger.Debug(
						"hand-written operation stubbed",
						"method",
						method,
						"path",
						prefix+path,
					)
				}
			}
		}
	}
}

// chiPatterns converts an OpenAPI path template to chi patterns. A trailing
// parameter is registered twice, once as a single segment and once as a
// wildcard, because Artifactory item paths ({itemPath}, {repoPath}) span
// segments and OpenAPI cannot say so.
func chiPatterns(template string) []string {
	// Artifactory reserves "api" as a repository key, so an item route such as
	// /artifactory/{repoKey}/{filePath} must never match /artifactory/api/...;
	// otherwise chi backtracks into it for every unknown API path and answers
	// 501 where the client should see 404 or 405. Go's regexp has no
	// lookahead, so "anything but api" is spelled out.
	template = strings.Replace(
		template,
		"/artifactory/{repoKey}/",
		"/artifactory/{repoKey:"+notAPI+"}/",
		1,
	)
	patterns := []string{template}
	segments := strings.Split(template, "/")
	last := segments[len(segments)-1]
	if strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}") {
		segments[len(segments)-1] = "*"
		patterns = append(patterns, strings.Join(segments, "/"))
	}
	return patterns
}

// notAPI matches one path segment other than the literal "api".
const notAPI = `(?:[^a/][^/]*|a(?:[^p/][^/]*)?|ap(?:[^i/][^/]*)?|api[^/]+)`

// registeredRoutes lists "METHOD pattern" for everything already on the router.
func registeredRoutes(r chi.Router) map[string]bool {
	routes := map[string]bool{}
	_ = chi.Walk(
		r,
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			routes[method+" "+route] = true
			return nil
		},
	)
	return routes
}

func handwrittenStub(method, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apierror.Write(w, surfaceFor(r.URL.Path), apierror.New(
			http.StatusNotImplemented,
			fmt.Sprintf("%s %s is specified but not implemented yet", method, path),
		))
	}
}

func loadEmbeddedSpec(name string) (*openapi3.T, error) {
	raw, err := api.Specs.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate %s: %w", name, err)
	}
	return doc, nil
}

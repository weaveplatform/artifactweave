package docker_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

func testHandler() http.Handler {
	return server.New(Config{
		BaseURL: "https://cfg.example.com", CompatVersion: "7.155.0",
		Revision: "v0.1.0-3-gabc",
	})
}

func loadSpec(t *testing.T, name string) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(repopath.Path(t, "api/"+name))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return doc
}

package docker_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/weaveplatform/artifactweave/internal/server"
)

func testHandler() http.Handler {
	return server.New(Config{
		BaseURL: "https://cfg.example.com", CompatVersion: "7.155.0",
		Revision: "v0.1.0-3-gabc",
	})
}

func loadSpec(t *testing.T, name string) *openapi3.T {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join(dir, "api", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return doc
}

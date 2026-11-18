package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestUnitRemoteCatalogResourceAndValidation(t *testing.T) {
	resource := RegistryCatalogResource()
	if resource.UpstreamPath != "_catalog" || resource.PermissionPath != "" || !resource.PermissionFolder || !resource.BlackoutOnly ||
		resource.Kind != source.ResourceKindVersions {
		t.Fatal(resource)
	}
	if image, op, ref, err := ParseRegistryPath("_catalog"); image != "" || op != "_catalog" || ref != "" || err != nil {
		t.Fatal(image, op, ref, err)
	}
	for _, body := range []string{`{`, `{}`, `{"repositories":null}`, `{"repositories":"wrong"}`, `{"repositories":[4]}`, `{"repositories":["UPPER"]}`} {
		c := RegistryContent{}
		q := source.Request{Resource: resource}
		err := c.prepare(t.Context(), q, source.Permit{}, remote.Options{}, &item.CacheUpload{Body: strings.NewReader(body), Length: int64(len(body))})
		if err == nil {
			t.Fatal(body)
		}
	}
}

func TestAccRemoteCatalogJoinsSafeUpstreamPages(t *testing.T) {
	for _, mode := range []string{"valid", "foreign", "path", "loop", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			mux := chi.NewRouter()
			calls := 0
			mux.Get("/v2/_catalog", func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				next := "/v2/_catalog?last=a"
				if mode == "foreign" {
					next = "https://other.invalid/v2/_catalog?last=a"
				}
				if mode == "path" {
					next = "/v2/else/tags/list?last=a"
				}
				if r.URL.RawQuery == "" || mode == "loop" {
					w.Header().Set("Link", "<"+next+">; rel=\"next\"")
				}
				if mode == "invalid" {
					_, _ = w.Write([]byte(`{"repositories":12}`))
					return
				}
				name := "a"
				if r.URL.RawQuery != "" {
					name = "b"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []string{name}})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			client, err := upstream.NewRegistry(
				repo.Config{URL: server.URL, Class: repo.Remote, PackageType: repo.Docker},
				upstream.RegistryOptions{
					Transport: remote.Options{Revision: "test", Prepare: func(*http.Request) error { return nil }, CheckURL: func(context.Context, *url.URL) error { return nil }},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.Do(t.Context(), "", remote.Request{Method: "GET", Path: "_catalog"})
			if err != nil {
				t.Fatal(err)
			}
			content := RegistryContent{Policy: &remote.Policy{}}
			result, err = content.pages(t.Context(), client, "", "_catalog", "", result)
			if mode != "valid" {
				if err == nil {
					_ = result.Body.Close()
					t.Fatal("accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer result.Body.Close()
			var doc struct {
				Repositories []string `json:"repositories"`
			}
			if err = json.NewDecoder(result.Body).Decode(&doc); err != nil || strings.Join(doc.Repositories, ",") != "a,b" || calls != 2 || result.Header.Get("Link") != "" {
				t.Fatal(doc, err, calls, result.Header)
			}
		})
	}
}

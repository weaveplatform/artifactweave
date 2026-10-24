package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccRegistryResourceValidation(t *testing.T) {
	for _, path := range []string{"image", "image/unknown/x", "image/manifests/bad tag", "image/blobs/latest", "image/referrers/sha256:no", "image/tags/other", "UPPER/manifests/latest"} {
		if _, _, _, err := registryResource(path); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	for _, op := range []string{"manifests", "blobs", "referrers"} {
		r, err := RegistryResource("team/image", op, "sha256:"+strings.Repeat("a", 64))
		if err != nil || r.Method != "GET" {
			t.Fatalf("%s: %+v %v", op, r, err)
		}
	}
}

func TestAccRegistryListingPaginationAndBoundaries(t *testing.T) {
	for _, mode := range []string{"tags", "referrers", "loop", "external", "wrong path", "unknown query", "bad JSON", "failure"} {
		t.Run(mode, func(t *testing.T) {
			router := chi.NewRouter()
			calls := 0
			operation, reference := "tags", "list"
			if mode == "referrers" {
				operation, reference = "referrers", "sha256:"+strings.Repeat("a", 64)
			}
			path := "/v2/team/image/" + operation + "/" + reference
			router.Get(path, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if mode == "referrers" {
					w.Header().Set("Content-Type", layout.MediaTypeOCIIndex)
				}
				if r.URL.RawQuery == "" || mode == "loop" {
					next := path + "?last=a"
					switch mode {
					case "external":
						next = "http://forbidden.invalid" + next
					case "wrong path":
						next = "/v2/other/tags/list?last=a"
					case "unknown query":
						next = path + "?token=secret"
					}
					w.Header().Set("Link", "<"+next+">; rel=\"next\"")
				}
				if r.URL.RawQuery != "" && mode == "failure" {
					w.WriteHeader(503)
					return
				}
				if mode == "bad JSON" {
					_, _ = io.WriteString(w, "{")
					return
				}
				if mode == "referrers" {
					_, _ = io.WriteString(
						w,
						`{"schemaVersion":2,"mediaType":"`+layout.MediaTypeOCIIndex+`","manifests":[]}`,
					)
					return
				}
				tag := "a"
				if r.URL.RawQuery != "" {
					tag = "b"
				}
				_ = json.NewEncoder(w).
					Encode(map[string]any{"name": "team/image", "tags": []string{tag}})
			})
			up := httptest.NewServer(router)
			t.Cleanup(up.Close)
			client, err := NewRegistry(
				repo.Config{URL: up.URL, Class: repo.Remote, PackageType: repo.Docker},
				RegistryOptions{
					Transport: Options{
						Revision: "test",
						Prepare:  func(*http.Request) error { return nil },
						CheckURL: func(context.Context, *url.URL) error { return nil },
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			res, err := client.Do(
				t.Context(),
				"team/image",
				Request{Method: "GET", Path: operation + "/" + reference},
			)
			if err != nil {
				t.Fatal(err)
			}
			content := RegistryContent{Policy: &Policy{}}
			res, err = content.pages(t.Context(), client, "team/image", operation, reference, res)
			if mode != "tags" && mode != "referrers" {
				if err == nil {
					_ = res.Body.Close()
					t.Fatal("unsafe or invalid page accepted")
				}
				if calls > 3 {
					t.Fatal("pagination did not stop")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || calls != 2 || res.Header.Get("Link") != "" {
				t.Fatalf("pagination: %s %v calls %d", body, err, calls)
			}
			if mode == "tags" && !strings.Contains(string(body), `["a","b"]`) {
				t.Fatal(string(body))
			}
		})
	}
}

func TestAccRegistryDescriptorIntegrityAndForeignPolicy(t *testing.T) {
	c := &RegistryContent{}
	q := source.Request{
		Repo: repo.Config{
			ExternalDependenciesEnabled:  new(true),
			ExternalDependenciesPatterns: []string{"https://layers.example/**"},
		},
	}
	opts := Options{CheckURL: func(context.Context, *url.URL) error { return nil }}
	d := layout.Descriptor{Digest: "sha256:" + strings.Repeat("a", 64), Size: 5}
	for _, mode := range []string{"valid", "digest", "negative size", "too many", "foreign blocked", "foreign invalid", "foreign allowed"} {
		t.Run(mode, func(t *testing.T) {
			m := layout.Manifest{Layers: []layout.Descriptor{d}}
			switch mode {
			case "digest":
				m.Layers[0].Digest = "sha512:bad"
			case "negative size":
				m.Layers[0].Size = -1
			case "too many":
				m.Layers = make([]layout.Descriptor, 4097)
			case "foreign blocked":
				m.Layers[0].URLs = []string{"https://elsewhere.example/layer"}
			case "foreign invalid":
				m.Layers[0].URLs = []string{"file:///secret"}
			case "foreign allowed":
				m.Layers[0].URLs = []string{"https://layers.example/layer"}
			}
			err := c.validateDescriptors(t.Context(), q, opts, m)
			if (mode == "valid" || mode == "foreign allowed") != (err == nil) {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	c.remember(
		source.Permit{}.Identity,
		"revision",
		"image",
		layout.Manifest{Layers: []layout.Descriptor{d}},
	)
	if _, ok := c.descriptor(source.Permit{}.Identity, "rotated", "image", d.Digest); ok {
		t.Fatal("descriptor crossed credential revision")
	}

	u := item.CacheUpload{Expected: types.Checksums{SHA256: strings.Repeat("a", 64)}}
	if err := c.prepareBlob(
		source.Permit{},
		"revision",
		"image",
		d.Digest,
		&u,
	); err != nil ||
		u.Validate == nil {
		t.Fatal(err)
	}
	if err := u.Validate(t.Context(), nil, 6); !errors.Is(err, item.ErrRemoteLength) {
		t.Fatal(err)
	}
	if err := u.Validate(t.Context(), nil, 5); err != nil {
		t.Fatal(err)
	}
	u.Declared.SHA256 = "bad"
	if err := c.prepareBlob(
		source.Permit{},
		"revision",
		"image",
		d.Digest,
		&u,
	); !errors.Is(
		err,
		item.ErrChecksumMismatch,
	) {
		t.Fatal(err)
	}
}

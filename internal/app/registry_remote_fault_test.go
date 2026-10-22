package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestRegistryRemoteForeignLayers(t *testing.T) {
	for _, mode := range []string{"allowed", "disabled", "pattern", "private destination", "redirect", "partial", "wrong digest"} {
		t.Run(mode, func(t *testing.T) {
			w := registryWorld(t)
			w.c.ExternalDependenciesEnabled = new(true)
			w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/foreign/**"}
			w.c.CustomHTTPHeaders = []repo.CustomHTTPHeader{
				{Name: "X-Remote-Secret", Value: "never-forward"},
			}
			w.c.Username, w.c.Password = "upstream", "secret"
			foreign := fakeupstream.New(t, fakeupstream.Options{})
			body := []byte("foreign-layer-bytes")
			url := w.up.URL + "/foreign/layer"
			if mode == "disabled" {
				w.c.ExternalDependenciesEnabled = new(false)
			}
			if mode == "pattern" {
				w.c.ExternalDependenciesPatterns = []string{w.up.URL + "/elsewhere/**"}
			}
			if mode == "private destination" {
				url = foreign.URL + "/layer"
				w.c.ExternalDependenciesPatterns = []string{foreign.URL + "/**"}
			}
			w.update()
			resource := fakeupstream.Resource{Body: body}
			if mode == "redirect" {
				resource.Redirect = foreign.URL + "/layer"
			}
			if mode == "partial" {
				resource.Fault.TruncateAfter = 4
			}
			if mode == "wrong digest" {
				resource.Body = []byte("foreign-layer-wrong")
			}
			w.up.Set("/foreign/layer", resource)
			config := []byte(`{}`)
			registryPut(w, "blobs", registryDigest(config), "application/octet-stream", config)
			manifest := registryJSON(
				t,
				map[string]any{
					"schemaVersion": 2,
					"mediaType":     layout.MediaTypeOCIManifest,
					"config": layout.Descriptor{
						MediaType: "application/vnd.oci.empty.v1+json",
						Digest:    registryDigest(config),
						Size:      int64(len(config)),
					},
					"layers": []layout.Descriptor{
						{
							MediaType: "application/vnd.oci.image.layer.nondistributable.v1.tar",
							Digest:    registryDigest(body),
							Size:      int64(len(body)),
							URLs:      []string{url},
						},
					},
				},
			)
			registryPut(w, "manifests", "foreign", layout.MediaTypeOCIManifest, manifest)
			got := w.request("GET", "/v2/remote/team/image/manifests/foreign", "reader", "")
			if mode == "disabled" || mode == "pattern" || mode == "private destination" {
				if got.Code == 200 {
					t.Fatal("unsafe foreign descriptor accepted")
				}
				return
			}
			remoteStatus(t, got, 200)
			got = w.request(
				"GET",
				"/v2/remote/team/image/blobs/"+registryDigest(body),
				"reader",
				"",
			)
			if mode == "allowed" {
				remoteStatus(t, got, 200)
				if got.Body.String() != string(body) {
					t.Fatal("foreign layer changed")
				}
			} else if got.Code == 200 {
				t.Fatal("invalid foreign response accepted")
			}
			for _, req := range w.up.Requests() {
				if req.Path == "/foreign/layer" &&
					(req.Header.Get("Authorization") != "" || req.Header.Get("X-Remote-Secret") != "" || req.Header.Get("Cookie") != "") {
					t.Fatal("foreign request received credentials", req)
				}
			}
			if len(foreign.Requests()) != 0 {
				t.Fatal("forbidden private destination contacted")
			}
		})
	}
}

func TestRegistryRemoteRevocationDuringFetch(t *testing.T) {
	for _, mutation := range []string{"permission", "user", "password", "parent revoked", "parent expired", "source"} {
		t.Run(mutation, func(t *testing.T) {
			w := registryWorld(t)
			var parent string
			if strings.HasPrefix(mutation, "parent") {
				issued, err := w.rt.tokens.Issue(
					t.Context(),
					token.Request{
						Username:  "reader",
						Scope:     types.UserScope(),
						ExpiresIn: new(time.Hour),
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				parent = issued.Record.ID
			}
			issued, err := w.rt.tokens.IssueRegistry(
				t.Context(),
				token.RegistryRequest{
					Username:  "reader",
					ExpiresIn: time.Hour,
					Parent:    parent,
					Access: types.RegistryAccess{
						Realm: "remote",
						Grants: []types.RegistryGrant{
							{
								Type:    "repository",
								Name:    "remote/team/image",
								Actions: []string{"pull"},
							},
						},
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("held registry blob")
			gate := new(fakeupstream.Barrier)
			t.Cleanup(gate.Release)
			w.up.Set(
				"/v2/team/image/blobs/"+registryDigest(body),
				fakeupstream.Resource{Body: body, Fault: fakeupstream.Fault{BeforeBody: gate}},
			)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- w.request("GET", "/v2/remote/team/image/blobs/"+registryDigest(body), "", "", "Authorization", "Bearer "+issued.AccessToken)
			}()
			select {
			case <-gate.Reached():
			case got := <-done:
				t.Fatalf("fetch returned before upstream: %d %s", got.Code, got.Body)
			case <-time.After(10 * time.Second):
				t.Fatal("fetch did not start")
			}
			switch mutation {
			case "permission":
				err = w.rt.store.Permissions().Delete(t.Context(), "remote-read")
			case "user", "password":
				u, e := w.rt.store.Users().Get(t.Context(), "reader")
				if e != nil {
					t.Fatal(e)
				}
				if mutation == "user" {
					u.Status = types.UserDisabled
				} else {
					u.PasswordHash = []byte("changed")
				}
				err = w.rt.store.Users().Update(t.Context(), u)
			case "parent revoked", "parent expired":
				p, e := w.rt.store.Tokens().Get(t.Context(), parent)
				if e != nil {
					t.Fatal(e)
				}
				err = w.rt.store.Tokens().Delete(t.Context(), parent)
				if err == nil && mutation == "parent expired" {
					p.ExpiresAt = time.Now().Add(-time.Hour)
					err = w.rt.store.Tokens().Create(t.Context(), p)
				}
			case "source":
				w.c.URL = w.up.URL + "/changed"
				w.update()
			}
			if err != nil {
				t.Fatal(err)
			}
			gate.Release()
			select {
			case got := <-done:
				if got.Code == http.StatusOK {
					t.Fatal("revoked read delivered bytes")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("fetch did not finish")
			}
			h := strings.TrimPrefix(registryDigest(body), "sha256:")
			if _, err = w.rt.store.Items().
				Get(t.Context(), "remote", true, layout.UploadPath("team/image", h)); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("revoked read published", err)
			}
		})
	}
}

package terraformhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func TestAccVirtualModuleVanityUsesStoredAndTransientSourceContracts(t *testing.T) {
	for _, mode := range []string{"stored-offline", "no-local-store", "upstream-no-store"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			virtualMembers(t, f.fixture, "remote")
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			if mode == "no-local-store" {
				c.StoreArtifactsLocally = new(false)
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			}
			f.location("1.0.0", "/vanity/start")
			header := http.Header{"Content-Type": {"text/html"}}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			f.up.Set("/vanity/start", fakeupstream.Resource{Header: header, Body: []byte(`<head><meta name="terraform-get" content="/archives/1.0.0.zip"></head>`)})
			endpoint := prefix + "v1/modules/virtual__ns/name/aws/1.0.0/download"
			metadata := f.do("GET", endpoint, "admin")
			expect(t, metadata, 204)
			public := metadata.Header().Get("X-Terraform-Get")
			expect(t, f.do("GET", public, ""), 200)
			calls := f.up.Count("", "/vanity/start")
			expect(t, f.do("GET", endpoint, "admin"), 204)
			if mode == "no-local-store" {
				if f.up.Count("", "/vanity/start") <= calls {
					t.Fatal("transient vanity control became a reusable cached result")
				}
				if _, err := f.db.Items().Get(t.Context(), "remote", true, "archives/1.0.0.zip"); err == nil {
					t.Fatal("transient vanity module retained an archive")
				}
				return
			}
			if f.up.Count("", "/vanity/start") != calls {
				t.Fatal("warm vanity resolution contacted upstream")
			}
			f.up.Close()
			c.Offline = new(true)
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			metadata = f.do("GET", endpoint, "admin")
			expect(t, metadata, 204)
			expect(t, f.do("GET", metadata.Header().Get("X-Terraform-Get"), ""), 200)
			virtualMembers(t, f.fixture, "local")
			expect(t, f.do("GET", public, ""), 403)
		})
	}
}

func TestAccModuleVanityBlockedReadPreservesCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"credential", "source", "metadata-zap", "virtual-membership", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", "/vanity/start")
			endpoint := remoteModuleBase + "1.0.0/download"
			if mode == "virtual-membership" {
				virtualMembers(t, f.fixture, "remote")
				endpoint = prefix + "v1/modules/virtual__ns/name/aws/1.0.0/download"
			}
			barrier := new(fakeupstream.Barrier)
			defer barrier.Release()
			f.up.Set("/vanity/start", fakeupstream.Resource{
				Body:   []byte("complete body required"),
				Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}},
				Fault:  fakeupstream.Fault{BeforeBody: barrier},
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				request := httptest.NewRequestWithContext(ctx, "GET", endpoint, nil)
				request.Header.Set("X-User", "reader")
				response := httptest.NewRecorder()
				f.h.ServeHTTP(response, request)
				done <- response
			}()
			select {
			case <-barrier.Reached():
			case <-time.After(10 * time.Second):
				t.Fatal("vanity response was not reached")
			}
			switch mode {
			case "credential":
				user, _ := f.db.Users().Get(t.Context(), "reader")
				user.PasswordHash = []byte("changed")
				if err := f.db.Users().Update(t.Context(), user); err != nil {
					t.Fatal(err)
				}
			case "source":
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				c.URL += "/changed-source"
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			case "metadata-zap":
				if _, err := f.db.RemoteCache().Invalidate(t.Context(), "remote", "", true); err != nil {
					t.Fatal(err)
				}
			case "virtual-membership":
				virtualMembers(t, f.fixture, "local")
			case "cancellation":
				cancel()
			}
			barrier.Release()
			select {
			case response := <-done:
				if response.Code >= 200 && response.Code < 300 || response.Header().Get("X-Terraform-Get") != "" {
					t.Fatalf("stale authority issued a module capability: status=%d", response.Code)
				}
				if strings.Contains(response.Body.String(), "complete body required") {
					t.Fatal("private vanity body reached the caller")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("interrupted vanity read did not stop")
			}
			if f.up.Count("", "/archives/1.0.0.zip") != 0 {
				t.Fatal("superseded vanity read acquired its archive")
			}
		})
	}
}

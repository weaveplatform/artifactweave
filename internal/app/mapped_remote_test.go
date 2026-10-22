package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestMappedMavenDescriptorDownloadAndAliasDenial(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint("no-store=", noStore), func(t *testing.T) {
			w := newMavenRemoteWorld(t)
			w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = "gradle-default", "maven-2-default"
			w.c.StoreArtifactsLocally = new(!noStore)
			w.update()
			body := sourcePOM("app", "1.0")
			mavenSource(w, "org/acme/app/1.0/app-1.0.pom", []byte(body), "application/xml")
			got := w.request(
				http.MethodGet,
				"/artifactory/remote/org.acme/app/ivy-1.0.xml",
				"reader",
				"",
			)
			remoteStatus(t, got, http.StatusOK)
			if got.Body.String() != body {
				t.Fatal("descriptor bytes changed during layout projection")
			}
			before := len(w.up.Requests())
			got = w.request(
				http.MethodGet,
				"/artifactory/remote/org.acme/app/1.0/app-1.0.pom",
				"reader",
				"",
			)
			if got.Code == http.StatusOK || len(w.up.Requests()) != before {
				t.Fatal("alternate canonical descriptor alias reached upstream", got.Code)
			}
			mavenSource(w, "org/acme/app/2.0/app-2.0.pom", []byte("not a POM"), "application/xml")
			got = w.request(
				http.MethodGet,
				"/artifactory/remote/org.acme/app/ivy-2.0.xml",
				"reader",
				"",
			)
			remoteStatus(t, got, http.StatusBadGateway)
			if _, err := w.rt.store.Items().
				Get(t.Context(), w.c.Key, true, "org.acme/app/ivy-2.0.xml"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("projected invalid POM admitted", err)
			}
		})
	}
}

func mappedRuntimeRequest(
	t *testing.T,
	w *remoteWorld,
) (context.Context, source.Request, *remote.Discovery) {
	t.Helper()
	p, err := w.rt.auth.Basic(t.Context(), "reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, err = auth.Authorize(
		t.Context(),
		authz.Sources{Permissions: w.rt.store.Permissions(), Groups: w.rt.store.Groups()},
		p,
	)
	if err != nil {
		t.Fatal(err)
	}
	d, err := remote.NewDiscovery(t.Context(), &item.Service{Store: w.rt.store}, w.c)
	if err != nil {
		t.Fatal(err)
	}
	return auth.WithPrincipal(
		t.Context(),
		p,
	), source.Request{
		Repo:    w.c,
		Subject: p.Subject(),
		Access:  p.Access(),
	}, d
}

func TestMappedRootRuntimePublicationAndDeliveryFences(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		for _, duringFetch := range []bool{false, true} {
			t.Run(fmt.Sprint(noStore, "/fetch=", duringFetch), func(t *testing.T) {
				w := newMavenRemoteWorld(t)
				const custom = "filename-discovery"
				if err := w.rt.store.Configs().
					Put(t.Context(), types.ConfigDocument{Kind: types.ConfigLayout, Key: custom, Document: json.RawMessage(`{"artifactPathPattern":"[org]/[module]-[baseRev].[ext]"}`)}); err != nil {
					t.Fatal(err)
				}
				w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = custom, "maven-2-default"
				w.c.StoreArtifactsLocally = new(!noStore)
				w.update()
				ctx, q, d := mappedRuntimeRequest(t, w)
				scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "app"})[0]
				if !scope.Root {
					t.Fatal("fixture did not exercise root authority")
				}
				resource := fakeupstream.Resource{
					Body:   []byte(`<a href="app-1.0.jar">jar</a>`),
					Header: http.Header{"Content-Type": {"text/html"}},
				}
				gate := new(fakeupstream.Barrier)
				t.Cleanup(gate.Release)
				if duringFetch {
					resource.Fault.BeforeBody = gate
				}
				w.up.Set("/org/acme/app/1.0/", resource)
				var got remote.MappedFolderListing
				done := make(chan error, 1)
				go func() {
					var err error
					got, err = remote.ListMappedFolder(
						ctx,
						w.rt.remoteSources,
						q,
						d,
						scope,
						"org/acme/app/1.0",
						new(remote.ListingBudget),
					)
					done <- err
				}()
				if duringFetch {
					select {
					case <-gate.Reached():
					case <-time.After(10 * time.Second):
						t.Fatal("mapped source did not start")
					}
				} else if err := <-done; err != nil {
					t.Fatal(err)
				}
				permission, err := w.rt.store.Permissions().Get(t.Context(), "remote-read")
				if err != nil {
					t.Fatal(err)
				}
				r := permission.Resources[types.ResourceArtifact]
				r.Targets[w.c.Key] = types.Target{Include: []string{"org.acme/app-1.0.jar"}}
				permission.Resources[types.ResourceArtifact] = r
				if err = w.rt.store.Permissions().Update(t.Context(), permission); err != nil {
					t.Fatal(err)
				}
				gate.Release()
				if duringFetch {
					if err = <-done; !errors.Is(err, source.ErrDenied) {
						t.Fatal("revoked root grant published metadata", err)
					}
				} else if err = got.Revalidate(ctx); !errors.Is(err, source.ErrDenied) {
					t.Fatal("surviving exact grant hid root revocation", err)
				}
				before := len(w.up.Requests())
				if _, err = remote.ListMappedFolder(
					ctx,
					w.rt.remoteSources,
					q,
					d,
					scope,
					"org/acme/app/1.0",
					new(remote.ListingBudget),
				); !errors.Is(err, source.ErrDenied) ||
					len(w.up.Requests()) != before {
					t.Fatal("stale principal acquired mapped source after root revocation", err)
				}
			})
		}
	}
}

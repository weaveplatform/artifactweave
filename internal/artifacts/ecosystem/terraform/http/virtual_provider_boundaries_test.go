package terraformhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func providerArchiveURL(t *testing.T, f *remoteProviderFixture, base, user string) string {
	t.Helper()
	response := f.get(user, base+"/download/linux/amd64")
	expect(t, response, 200)
	var doc struct {
		URL string `json:"download_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.URL
}

func TestAccVirtualProviderSignedPrincipalAndItemFaults(t *testing.T) {
	for _, fault := range []string{"user", "permissions", "download"} {
		t.Run(fault, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			direct := providerArchiveURL(t, f, prefix+"provider/v1/providers/hashicorp/null/2.0.0", "admin")
			virtual := providerArchiveURL(t, f, virtualProviderBase+"2.0.0", "admin")
			f.read = failingStore{f.db, fault}
			f.h = f.fixture.router(f.keys)
			expect(t, f.get("", direct), 500)
			expect(t, f.get("", virtual), 500)
		})
	}
}

func TestAccVirtualProviderRemoteSignedSourceFaultAndRefusal(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	target := providerArchiveURL(t, f, virtualProviderBase+"1.0.0", "reader")
	resolve := f.svc.Resolve
	f.svc.Resolve = func(context.Context, repo.Config) (model.SourceIdentity, error) {
		return model.SourceIdentity{}, errors.Join(kernel.ErrStore, context.DeadlineExceeded)
	}
	expect(t, f.get("", target), 500)
	f.svc.Resolve = resolve
	// The same valid claim becomes a refusal when the source incarnation changes.
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.URL = f.up.URL + "/replacement" })
	response := f.get("", target)
	expect(t, response, 403)
	if response.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
		t.Fatal(response.Body.String())
	}
}

func TestAccVirtualProviderSignedBoundaryRefusals(t *testing.T) {
	for _, boundary := range []string{"wrong-repository", "forged-signature", "nested-removal", "member-module", "member-pattern", "virtual-pattern", "signer-read", "hosted-remote-claim", "hosted-kind", "hosted-version"} {
		t.Run(boundary, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			permission, err := f.db.Permissions().Get(t.Context(), "read")
			if err != nil {
				t.Fatal(err)
			}
			resource := permission.Resources[api.ResourceArtifact]
			resource.Targets["provider"] = accesstest.Target{}
			permission.Resources[api.ResourceArtifact] = resource
			if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			inner := *repo.Defaults(&repo.Config{Key: "inner", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"provider"}})
			if err := f.db.Repos().Create(t.Context(), inner); err != nil {
				t.Fatal(err)
			}
			virtualMembers(t, f.fixture, "inner")
			target := providerArchiveURL(t, f, virtualProviderBase+"2.0.0", "reader")
			claim := providerURLClaim(t, f, target)
			switch boundary {
			case "wrong-repository":
				target = strings.Replace(target, "/virtual/providers/", "/mods/providers/", 1)
			case "forged-signature":
				target += "broken"
			case "nested-removal":
				patchRepo(t, f.fixture, "inner", func(c *repo.Config) { c.Repositories = nil })
			case "member-module":
				patchRepo(t, f.fixture, "provider", func(c *repo.Config) { c.TerraformType = repo.TerraformModule })
			case "member-pattern":
				patchRepo(t, f.fixture, "provider", func(c *repo.Config) { c.ExcludesPattern = new("**/*.zip") })
			case "virtual-pattern":
				patchRepo(t, f.fixture, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("**/*.zip") })
			case "signer-read":
				delete(resource.Targets, "provider")
				permission.Resources[api.ResourceArtifact] = resource
				if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
					t.Fatal(err)
				}
			default:
				if boundary == "hosted-remote-claim" {
					claim.Source = &model.SourceIdentity{}
				}
				if boundary == "hosted-kind" {
					claim.Provider.Kind = 1
				}
				if boundary == "hosted-version" {
					claim.Provider.Version = "9.0.0"
				}
				h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
				raw, err := h.signDownloadURL(httptest.NewRequest(http.MethodGet, target, nil), claim)
				if err != nil {
					t.Fatal(err)
				}
				target = raw
			}
			response := f.get("", target)
			expect(t, response, 403)
			if response.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
				t.Fatal(response.Body.String())
			}
		})
	}
}

func TestAccVirtualProviderDeferredControls(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	for _, request := range []struct{ method, path string }{{"POST", virtualProviderBase + "versions"}, {"PUT", virtualProviderBase + "bad/download/linux/amd64"}, {"GET", virtualProviderBase + "bad/download/linux/amd64"}, {"GET", prefix + "virtual/providers/not-a-host/hashicorp/null/bad.json"}} {
		response := f.do(request.method, request.path, "admin")
		expect(t, response, 501)
	}
	target := providerArchiveURL(t, f, virtualProviderBase+"2.0.0", "admin")
	expect(t, f.do("POST", target, ""), 405)
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	u.Path += ";prop=value"
	expect(t, f.get("", u.String()), 403)
}

func TestAccVirtualProviderSignedSourcePolicyRefusal(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	target := providerArchiveURL(t, f, virtualProviderBase+"1.0.0", "reader")
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.ExcludesPattern = new("storage/**") })
	response := f.get("", target)
	expect(t, response, 403)
	if response.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
		t.Fatal(response.Body.String())
	}
}

func TestAccVirtualModuleSignedSourcePolicyRefusal(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	virtualMembers(t, f.fixture, "remote")
	response := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "reader")
	expect(t, response, 204)
	target := response.Header().Get("X-Terraform-Get")
	claim := decodeVirtualClaim(t, target)
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.ExcludesPattern = new(claim.Path) })
	refused := f.do("GET", target, "")
	expect(t, refused, 403)
	if refused.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
		t.Fatal(refused.Body.String())
	}
}

func TestAccVirtualSignedTransientErrorsKeepDirectControls(t *testing.T) {
	for _, kind := range []string{"provider", "module"} {
		for _, failure := range []struct {
			name   string
			err    error
			status int
		}{
			{"unavailable", source.ErrUnavailable, 502},
			{"timeout", context.DeadlineExceeded, 504},
			{"superseded", source.ErrSuperseded, 503},
			{"storage", errors.Join(format.ErrStorageFault, errors.New("local storage")), 500},
			{"missing", kernel.ErrNotFound, 404},
			{"denied", source.ErrDenied, 403},
		} {
			t.Run(kind+"/"+failure.name, func(t *testing.T) {
				var f *remoteFixture
				var direct, public string
				if kind == "provider" {
					p, _ := virtualProviderFixture(t)
					f = p.remoteFixture
					direct = providerArchiveURL(t, p, remoteProviderBase+"1.0.0", "reader")
					public = providerArchiveURL(t, p, virtualProviderBase+"1.0.0", "reader")
				} else {
					f = newRemoteFixture(t)
					f.modules("1.0.0")
					virtualMembers(t, f.fixture, "remote")
					direct = f.signed("1.0.0", "reader")
					metadata := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "reader")
					expect(t, metadata, 204)
					public = metadata.Header().Get("X-Terraform-Get")
				}
				f.svc.HTTP = providerRotationBackend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, failure.err
				})
				expect(t, f.do("GET", direct, ""), failure.status)
				response := f.do("GET", public, "")
				want := failure.status
				if failure.name == "missing" || failure.name == "denied" {
					want = 403
				}
				expect(t, response, want)
				if want == 403 && response.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
					t.Fatal(response.Body.String())
				}
				if failure.name == "superseded" && response.Header().Get("Retry-After") != "1" {
					t.Fatal(response.Header())
				}
			})
		}
	}
}

func TestAccVirtualSignedUpstreamOutageKeepsDirectControls(t *testing.T) {
	for _, kind := range []string{"provider", "module"} {
		t.Run(kind, func(t *testing.T) {
			var f *remoteFixture
			var direct, public, metadataPath string
			if kind == "provider" {
				p, _ := virtualProviderFixture(t)
				f = p.remoteFixture
				metadataPath = "/registry/providers/hashicorp/null/1.0.0/download/linux/amd64"
				body, err := json.Marshal(p.metadata)
				if err != nil {
					t.Fatal(err)
				}
				f.up.Set(metadataPath, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"must-revalidate"}}})
				direct = providerArchiveURL(t, p, remoteProviderBase+"1.0.0", "reader")
				public = providerArchiveURL(t, p, virtualProviderBase+"1.0.0", "reader")
			} else {
				f = newRemoteFixture(t)
				f.modules("1.0.0")
				virtualMembers(t, f.fixture, "remote")
				metadataPath = "/registry/modules/ns/name/aws/1.0.0/download"
				f.up.Set(metadataPath, fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}, "Cache-Control": {"must-revalidate"}}})
				direct = f.signed("1.0.0", "reader")
				metadata := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "reader")
				expect(t, metadata, 204)
				public = metadata.Header().Get("X-Terraform-Get")
			}
			patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.RetrievalCachePeriodSecs = new(0); c.MissedRetrievalCachePeriodSecs = new(0) })
			f.up.Set(metadataPath, fakeupstream.Resource{Status: 503, Header: http.Header{"Retry-After": {"2"}}})
			before := f.up.Count("GET", metadataPath)
			var directRetry string
			for i, target := range []string{direct, public} {
				response := f.do("GET", target, "")
				expect(t, response, 503)
				if i == 0 {
					directRetry = response.Header().Get("Retry-After")
				} else if response.Header().Get("Retry-After") != directRetry {
					t.Fatal("virtual changed direct retry headers", response.Header())
				}
			}
			if f.up.Count("GET", metadataPath) != before+2 {
				t.Fatal("outage controls did not reach real upstream")
			}
		})
	}
}

package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
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

func TestUnitForeignLayerResource(t *testing.T) {
	base := Descriptor{
		Digest:    "sha256:" + strings.Repeat("a", 64),
		Size:      0,
		MediaType: "application/vnd.oci.image.layer.nondistributable.v1.tar",
		URLs:      []string{"https://layers.example/layer?token=opaque"},
	}
	for _, mode := range []string{"valid", "ordinary", "missing URL", "negative", "bad digest", "bad image", "scheme", "credentials", "fragment", "bad URL"} {
		t.Run(mode, func(t *testing.T) {
			d := base
			image := "team/image"
			switch mode {
			case "ordinary":
				d.MediaType = MediaTypeOCIManifest
			case "missing URL":
				d.URLs = nil
			case "negative":
				d.Size = -1
			case "bad digest":
				d.Digest = "bad"
			case "bad image":
				image = "BAD"
			case "scheme":
				d.URLs = []string{"file:///secret"}
			case "credentials":
				d.URLs = []string{"https://u:p@layers.example/file"}
			case "fragment":
				d.URLs = []string{"https://layers.example/file#fragment"}
			case "bad URL":
				d.URLs = []string{"https://%zz"}
			}
			r, err := ForeignLayerResource(image, d)
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid resource accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.ExternalURL != d.URLs[0] || r.UpstreamPath != "" || r.Checksums.SHA256 != strings.Repeat("a", 64) || r.Validate == nil {
				t.Fatal(r)
			}
			if r.Validate(t.Context(), nil, 0) != nil || !errors.Is(r.Validate(t.Context(), nil, 1), source.ErrInvalidResult) {
				t.Fatal("exact descriptor size not held")
			}
			c := RegistryContent{}
			u := item.CacheUpload{Length: -1}
			if err = c.prepare(t.Context(), source.Request{Resource: r}, source.Permit{}, remote.Options{}, &u); err != nil || u.MaxBytes != 1 {
				t.Fatal("zero descriptor has no byte bound", u.MaxBytes, err)
			}
			u.Length = 1
			if err = c.prepare(t.Context(), source.Request{Resource: r}, source.Permit{}, remote.Options{}, &u); !errors.Is(err, item.ErrRemoteLength) {
				t.Fatal(err)
			}
			if _, ok := ForeignLayer(Manifest{Config: &d}, d.Digest); ok {
				t.Fatal("config selected")
			}
			if _, ok := ForeignLayer(Manifest{Layers: []Descriptor{d}}, d.Digest); !ok {
				t.Fatal("foreign layer absent")
			}
			for _, bad := range []source.Resource{{}, {ExternalURL: r.ExternalURL, Variant: foreignVariant + "!!"}, {ExternalURL: r.ExternalURL, Variant: foreignVariant + "e30"}, func() source.Resource { x := r; x.ExternalURL += "x"; return x }()} {
				if _, err := foreignResource(bad); err == nil {
					t.Fatal("invalid evidence accepted")
				}
			}
		})
	}
}

func TestAccForeignTransportStripsSameOriginAndRedirectSecrets(t *testing.T) {
	router := chi.NewRouter()
	calls := 0
	router.Get("/foreign/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "upstream", Value: "secret"})
		http.Redirect(w, r, "/foreign/final", 302)
	})
	router.Get("/foreign/final", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Remote-Secret") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Custom") != "" {
			t.Error("foreign redirect received secrets", r.Header)
		}
		_, _ = io.WriteString(w, "verified")
	})
	up := httptest.NewServer(router)
	t.Cleanup(up.Close)
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(up.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret"}})
	c := repo.Config{
		URL:                          up.URL,
		Class:                        repo.Remote,
		PackageType:                  repo.Docker,
		ExternalDependenciesEnabled:  new(true),
		ExternalDependenciesPatterns: []string{up.URL + "/foreign/**"},
	}
	content := RegistryContent{Registries: &upstream.RegistryClients{}}
	opts := remote.Options{Jar: jar, CheckURL: func(context.Context, *url.URL) error { return nil }, Prepare: func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Cookie", "secret=1")
		r.Header.Set("X-Remote-Secret", "secret")
		r.Header.Set("X-Custom", "custom")
		return nil
	}}
	res, err := content.external(t.Context(), c, opts, up.URL+"/foreign/start")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || string(body) != "verified" || calls != 1 {
		t.Fatal(string(body), err, calls)
	}
}

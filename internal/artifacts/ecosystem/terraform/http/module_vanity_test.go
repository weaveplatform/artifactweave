package terraformhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitModuleHTMLSourceSelectionAndRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, html, want string }{
		{"plain", `<head><meta name="terraform-get" content="/archive.zip"></head>`, "/archive.zip"},
		{"mixed-case", `<HEAD><META NAME="terraform-get" CONTENT="/archive.zip"></HEAD>`, "/archive.zip"},
		{"entities-once", `<head><meta name="terraform-get" content="/archive.zip?x=1&amp;y=&amp;amp;"></head>`, "/archive.zip?x=1&y=&amp;"},
		{"empty-before-first", `<head><meta name="terraform-get" content=""><meta name="terraform-get" content="/first.zip"><meta name="terraform-get" content="/second.zip"></head>`, "/first.zip"},
		{"ignore-other-name", `<head><meta name="other" content="/wrong.zip"><meta name="terraform-get" content="/archive.zip"></head>`, "/archive.zip"},
		{"body-cutoff", `<head></head><body><meta name="terraform-get" content="/wrong.zip"></body>`, ""},
		{"body-without-head", `<body><meta name="terraform-get" content="/wrong.zip"></body>`, ""},
		{"name-value-case-sensitive", `<head><meta name="TERRAFORM-GET" content="/wrong.zip"></head>`, ""},
		{"duplicate-name", `<head><meta name="other" NAME="terraform-get" content="/wrong.zip"></head>`, ""},
		{"duplicate-content", `<head><meta name="terraform-get" content="/first.zip" CONTENT="/second.zip"></head>`, ""},
		{"missing", `<head><title>no source</title></head>`, ""},
		{"malformed", `<head><meta name="terraform-get" content="/bad.zip`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := moduleHTMLSource([]byte(tc.html))
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid or missing source was accepted")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("selected %q, expected %q: %v", got, tc.want, err)
			}
		})
	}
}

func TestUnitModuleVanitySourceGrammar(t *testing.T) {
	t.Parallel()
	c := repo.Config{URL: "https://up.example", TerraformRegistryURL: "https://registry.example"}
	base := "https://up.example/effective/document"
	for _, raw := range []string{"https://up.example/vanity?child=only", "/vanity", "./vanity", "../vanity", "?child=only", "/vanity?terraform-get=1"} {
		got, err := parseModuleSource(raw, base, c)
		if err != nil || !got.Vanity || got.Archive != "" || got.Path != "" || strings.Contains(got.Target, "terraform-get") {
			t.Fatal("qualified vanity source refused", got, err)
		}
	}
	for _, raw := range []string{"registry.terraform.io/ns/name/aws", "ns/name/aws", "./vanity?archive=false", "/module.zip?archive=", "/vanity?archive=unsupported", "/vanity?terraform-get=2", "/vanity?terraform-get=1&terraform-get=1"} {
		if got, err := parseModuleSource(raw, base, c); err == nil {
			t.Fatal("unqualified source admitted", got)
		}
	}
	identity, err := source.ModuleVanityIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	control, _ := json.Marshal(
		source.ModuleVanityResponse{
			HTML:     []byte(`<head><meta name="terraform-get" content="file:///bad"><meta name="terraform-get" content="/good.zip"></head>`),
			Identity: identity,
		},
	)
	if _, _, err := parseModuleVanity(bytes.NewReader(control), base, c); err == nil {
		t.Fatal("later metadata rescued an invalid first selected source")
	}
	control, _ = json.Marshal(source.ModuleVanityResponse{Source: "?archive=zip&child=owned", Identity: identity})
	got, _, err := parseModuleVanity(bytes.NewReader(control), base, c)
	if err != nil || got.Target != base+"?child=owned" {
		t.Fatal("query-only source did not retain only its explicit child query", got, err)
	}
}

func TestAccRemoteModuleVanityHeaderHTMLRedirectAndOffline(t *testing.T) {
	for _, mode := range []string{"header", "html", "redirect-relative", "chain"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", "/vanity/start?parent=private")
			var invalidQuery atomic.Bool
			options := f.policy.Options
			f.policy.Options = func(ctx context.Context, c repo.Config) (remote.Options, error) {
				opts, err := options(ctx, c)
				prepare := opts.Prepare
				opts.Prepare = func(request *http.Request) error {
					if prepare != nil {
						if err := prepare(request); err != nil {
							return err
						}
					}
					if strings.HasPrefix(request.URL.Path, "/vanity/") {
						values := request.URL.Query()["terraform-get"]
						if len(values) != 1 || values[0] != "1" {
							invalidQuery.Store(true)
						}
					}
					return nil
				}
				return opts, err
			}
			header := func(next string) fakeupstream.Resource {
				return fakeupstream.Resource{Body: []byte("header-selected body"), Header: http.Header{"X-Terraform-Get": {next}, "Content-Type": {"application/octet-stream"}}}
			}
			next := "/archives/1.0.0.zip?child=owned"
			switch mode {
			case "header":
				f.up.Set("/vanity/start", header(next))
			case "html":
				f.up.Set(
					"/vanity/start",
					fakeupstream.Resource{
						Body:   []byte(`<head><META NAME="terraform-get" CONTENT="/archives/1.0.0.zip?child=owned"></head><body>ignored</body>`),
						Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
					},
				)
			case "redirect-relative":
				f.up.Set("/vanity/start", fakeupstream.Resource{Redirect: "/effective/directory/doc?redirect=private"})
				f.up.Set("/effective/directory/doc", header("../../archives/1.0.0.zip?child=owned"))
			case "chain":
				f.up.Set("/vanity/start", header("/vanity/next?child=intermediate"))
				f.up.Set("/vanity/next", header(next))
			}
			location := f.signed("1.0.0", "admin")
			response := f.do("GET", location, "")
			expect(t, response, 200)
			if !bytes.Equal(response.Body.Bytes(), archiveZip(t, "main.tf")) {
				t.Fatal("vanity resolution changed original archive bytes")
			}
			for _, request := range f.up.Requests() {
				if strings.HasPrefix(request.Path, "/vanity/") && request.Method == "GET" {
					if !request.Query.Has("terraform-get") {
						t.Fatal("vanity request did not carry exactly one opt-in")
					}
				}
				if request.Path == "/archives/1.0.0.zip" &&
					(request.Query.Has("terraform-get") || request.Query.Has("parent") || request.Query.Has("redirect") || !request.Query.Has("child")) {
					t.Fatal("archive inherited parent controls or lost its own query")
				}
			}
			if invalidQuery.Load() {
				t.Fatal("vanity query did not contain exactly one terraform-get=1")
			}
			f.up.Close()
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			c.Offline = new(true)
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			location = f.signed("1.0.0", "admin")
			expect(t, f.do("GET", location, ""), 200)
		})
	}
}

func TestAccRemoteModuleVanityBoundsAndCyclesBeforeContact(t *testing.T) {
	for _, tc := range []struct {
		name string
		hops int
		loop string
	}{
		{"ten-documents", 10, ""},
		{"eleventh-refused", 11, ""},
		{"self", 1, "/vanity/0"},
		{"two-node", 2, "/vanity/0"},
		{"redirect-assisted", 1, "/vanity/alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", "/vanity/0")
			for i := range tc.hops {
				next := "/vanity/" + strconv.Itoa(i+1)
				if i == tc.hops-1 {
					next = "/archives/1.0.0.zip"
					if tc.loop != "" {
						next = tc.loop
					}
				}
				f.up.Set("/vanity/"+strconv.Itoa(i), fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {next}}})
			}
			if tc.name == "redirect-assisted" {
				f.up.Set("/vanity/0", fakeupstream.Resource{Redirect: "/vanity/alias"})
				f.up.Set("/vanity/alias", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {"/vanity/alias"}}})
			}
			response := f.do("GET", remoteModuleBase+"1.0.0/download", "admin")
			if tc.name == "ten-documents" {
				expect(t, response, 204)
			} else {
				expect(t, response, 502)
			}
			counts := map[string]int{}
			for _, request := range f.up.Requests() {
				if strings.HasPrefix(request.Path, "/vanity/") {
					counts[request.Path]++
					if request.Path == "/vanity/10" || counts[request.Path] > 1 {
						t.Fatal("hop/cycle limit allowed another origin contact", counts)
					}
				}
			}
		})
	}
}

func TestAccRemoteModuleVanityInvalidResponseRetriesWithoutArchivePoison(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response fakeupstream.Resource
	}{
		{"duplicate-header", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip", "/bad.zip"}}}},
		{"malformed-html", fakeupstream.Resource{Body: []byte(`<head><meta name="terraform-get" content="file:///bad"></head>`), Header: http.Header{"Content-Type": {"text/html"}}}},
		{"unsupported-mime", fakeupstream.Resource{Body: []byte(`{"source":"/archives/1.0.0.zip"}`), Header: http.Header{"Content-Type": {"application/json"}}}},
		{"unsafe-destination", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {"http://169.254.169.254/archive.zip"}}}},
		{"truncated-body-with-valid-header", fakeupstream.Resource{Body: []byte(strings.Repeat("x", 512)), Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}}, Fault: fakeupstream.Fault{TruncateAfter: 64}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			f.location("1.0.0", "/vanity/start")
			f.up.Set("/vanity/start", tc.response)
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 502)
			if f.up.Count("", "/archives/1.0.0.zip") != 0 {
				t.Fatal("invalid metadata contacted archive")
			}
			f.up.Set("/vanity/start", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}}})
			expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 204)
		})
	}
}

func TestUnitModuleVanityControlRejectsMalformedPrivateMetadata(t *testing.T) {
	for _, raw := range []string{
		`{`, `null`, `{}`, `{"identity":"bad","source":"/archive.zip"}`,
		`{"identity":"` + strings.Repeat("z", 64) + `","source":"/archive.zip"}`,
		`{"identity":"` + strings.Repeat("a", 64) + `","source":"/archive.zip","unknown":true}`,
		`{"identity":"` + strings.Repeat("a", 64) + `","source":"/archive.zip","html":"YmFk"}`,
		`{"identity":"` + strings.Repeat("a", 64) + `","html":"/w=="}`,
		`{"identity":"` + strings.Repeat("a", 64) + `","source":"/archive.zip"} {}`,
	} {
		if _, _, err := parseModuleVanity(strings.NewReader(raw), "https://up.example/document", repo.Config{URL: "https://up.example"}); err == nil {
			t.Fatal("malformed private control was admitted")
		}
	}
	if _, _, err := parseModuleVanity(brokenModuleReader{}, "https://up.example/document", repo.Config{URL: "https://up.example"}); err == nil {
		t.Fatal("private control read failure was ignored")
	}
}

func TestAccRemoteModuleVanityCombinesSelectorsAndChecksumPins(t *testing.T) {
	for _, mode := range []string{"matching-pin", "conflicting-pin", "outer-selector", "conflicting-intermediate-pin", "oversized-combined-selector"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemoteFixture(t)
			f.modules("1.0.0")
			archive := archiveZip(t, "root/modules/demo/main.tf")
			digest := sha256.Sum256(archive)
			pin := fmt.Sprintf("sha256:%x", digest)
			initial := "/vanity/start?checksum=" + pin
			selected := "/archives/1.0.0.zip//root/modules/demo?checksum=" + pin
			if mode == "conflicting-pin" {
				selected = "/archives/1.0.0.zip?checksum=sha256:" + strings.Repeat("a", 64)
			}
			if mode == "outer-selector" {
				initial = "/vanity/start//demo?checksum=" + pin
				selected = "/archives/1.0.0.zip//root/modules?checksum=" + pin
			}
			if mode == "conflicting-intermediate-pin" {
				selected = "/vanity/second?checksum=sha256:" + strings.Repeat("a", 64)
				f.up.Set("/vanity/second", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {"/archives/1.0.0.zip"}}})
			}
			if mode == "oversized-combined-selector" {
				initial = "/vanity/start//" + strings.Repeat("a", 4100)
				selected = "/archives/1.0.0.zip//" + strings.Repeat("b", 4100)
			}
			f.location("1.0.0", initial)
			f.up.Set("/vanity/start", fakeupstream.Resource{Status: 200, Header: http.Header{"X-Terraform-Get": {selected}}})
			f.up.Set("/archives/1.0.0.zip", fakeupstream.Resource{Body: archive, Header: http.Header{"Content-Type": {"application/zip"}}})
			metadata := f.do("GET", remoteModuleBase+"1.0.0/download", "admin")
			if mode == "conflicting-pin" || mode == "conflicting-intermediate-pin" || mode == "oversized-combined-selector" {
				expect(t, metadata, 502)
				if f.up.Count("", "/archives/1.0.0.zip") != 0 {
					t.Fatal("conflicting pins allowed archive acquisition")
				}
				if mode == "conflicting-intermediate-pin" && f.up.Count("", "/vanity/second") != 0 {
					t.Fatal("conflicting intermediate pin allowed another metadata fetch")
				}
				return
			}
			expect(t, metadata, 204)
			if !strings.Contains(metadata.Header().Get("X-Terraform-Get"), "//root/modules/demo?") {
				t.Fatal("outer selector was lost")
			}
		})
	}
	a := kernel.Checksums{SHA256: strings.Repeat("a", 64)}
	if got, err := combineModuleChecksums(a, kernel.Checksums{SHA1: strings.Repeat("b", 40)}); err != nil || got.SHA256 != a.SHA256 || got.SHA1 == "" || a.SHA1 != "" {
		t.Fatal("independent checksum constraints were lost or mutated", err)
	}
}

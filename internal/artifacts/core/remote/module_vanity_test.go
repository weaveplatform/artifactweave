package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

func vanityRequest(w *policyWorld) source.Request {
	options := w.p.Options
	w.p.Options = func(ctx context.Context, c repo.Config) (remote.Options, error) {
		opts, err := options(ctx, c)
		opts.CheckURL = func(_ context.Context, target *url.URL) error {
			if target.Scheme+"://"+target.Host != w.up.URL {
				return remote.ErrSecurity
			}
			return nil
		}
		return opts, err
	}
	q := w.request("module/vanity", source.ResourceKindMetadata)
	q.Resource.ModuleVanityDocument = true
	q.Resource.ExternalURL = w.up.URL + "/module/vanity?terraform-get=1"
	q.Resource.CheckURL = func(context.Context, *url.URL) error { return nil }
	q.Resource.Variant = "module-vanity-test-v1"
	q.Resource.ExpectedContentType = "application/json"
	q.Resource.ValidateMetadata = func(_ context.Context, body io.ReadSeeker, _ int64, actual source.MetadataResponse) error {
		var control source.ModuleVanityResponse
		if json.NewDecoder(body).Decode(&control) != nil || control.Source != "../archive.zip" ||
			len(control.HTML) != 0 || len(control.Identity) != 64 || actual.BaseURL != w.up.URL+"/effective/vanity" {
			return source.ErrInvalidResult
		}
		return nil
	}
	return q
}

func TestAccModuleVanityProjectionRetainsCompleteAdmissionAndCacheFences(t *testing.T) {
	for _, mode := range []string{"stored", "upstream-no-store", "no-local-store", "zap-during-admission"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := vanityRequest(w)
			if mode == "no-local-store" {
				q.Repo.StoreArtifactsLocally = ptr(false)
			}
			if mode == "zap-during-admission" {
				validate := q.Resource.ValidateMetadata
				q.Resource.ValidateMetadata = func(ctx context.Context, r io.ReadSeeker, n int64, m source.MetadataResponse) error {
					if err := validate(ctx, r, n, m); err != nil {
						return err
					}
					_, err := w.db.RemoteCache().Invalidate(ctx, q.Repo.Key, "", true)
					return err
				}
			}
			w.up.Set("/module/vanity", fakeupstream.Resource{Redirect: "/effective/vanity?private=query"})
			header := http.Header{"X-Terraform-Get": {"../archive.zip"}, "Content-Type": {"application/octet-stream"}}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			w.up.Set("/effective/vanity", fakeupstream.Resource{Header: header, Body: []byte("header takes precedence"), ETag: `"source"`})
			result, err := w.read(q)
			if mode == "zap-during-admission" {
				if !errors.Is(err, source.ErrSuperseded) {
					t.Fatal("metadata zap did not fence vanity publication", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(result.Stream.Body)
			_ = result.Stream.Body.Close()
			if err != nil || strings.Contains(string(raw), "header takes precedence") || strings.Contains(string(raw), "private=query") {
				t.Fatal("projection retained discarded body or response-base query", err)
			}
			entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", kernel.Page{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "no-local-store" {
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" {
						t.Fatal("transient vanity document was persisted")
					}
				}
				return
			}
			calls := len(w.up.Requests())
			w.mustRead(q, string(raw))
			if len(w.up.Requests()) != calls {
				t.Fatal("fresh vanity cache contacted its origin")
			}
			w.clock.Advance(time.Minute)
			w.mustRead(q, string(raw))
			for _, request := range w.up.Requests() {
				if request.Header.Get("If-None-Match") != "" || request.Header.Get("If-Modified-Since") != "" {
					t.Fatal("projected bytes borrowed upstream validators")
				}
			}
			q.Repo.Offline = ptr(true)
			w.up.Close()
			w.mustRead(q, string(raw))
		})
	}
}

func TestUnitModuleVanityProjectionBoundsGrammarAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		header  http.Header
		body    string
		allowed bool
	}{
		{"header-wins", 200, http.Header{"X-Terraform-Get": {"/package.zip"}}, "<html>ignored</html>", true},
		{"html", 200, http.Header{"Content-Type": {"text/html; charset=UTF-8"}}, "<head></head>", true},
		{"xhtml", 200, http.Header{"Content-Type": {"application/xhtml+xml"}}, "<head></head>", true},
		{"ascii", 200, http.Header{"Content-Type": {"text/html; charset=us-ascii"}}, "<head></head>", true},
		{"wrong-status", 204, http.Header{"X-Terraform-Get": {"/package.zip"}}, "", false},
		{"wrong-mime", 200, http.Header{"Content-Type": {"application/json"}}, "{}", false},
		{"missing-mime", 200, nil, "<head></head>", false},
		{"empty-header", 200, http.Header{"X-Terraform-Get": {""}, "Content-Type": {"text/html"}}, "<head></head>", false},
		{"duplicate-header", 200, http.Header{"X-Terraform-Get": {"/a.zip", "/b.zip"}}, "", false},
		{"invalid-utf8-header", 200, http.Header{"X-Terraform-Get": {string([]byte{255})}}, "", false},
		{"unsupported-charset", 200, http.Header{"Content-Type": {"text/html; charset=iso-8859-1"}}, "<head></head>", false},
		{"non-ascii", 200, http.Header{"Content-Type": {"text/html; charset=us-ascii"}}, "<head>é</head>", false},
		{"invalid-utf8-body", 200, http.Header{"Content-Type": {"text/html"}}, string([]byte{255}), false},
		{"oversized-header", 200, http.Header{"X-Terraform-Get": {strings.Repeat("x", 8193)}}, "", false},
		{"maximum-header", 200, http.Header{"X-Terraform-Get": {"/" + strings.Repeat("x", 8191)}}, "", true},
		{"maximum-body", 200, http.Header{"X-Terraform-Get": {"/package.zip"}}, strings.Repeat("x", source.MaxModuleVanityDocumentBytes), true},
		{"maximum-html", 200, http.Header{"Content-Type": {"text/html"}}, strings.Repeat("<", source.MaxModuleVanityDocumentBytes), true},
		{"oversized-body", 200, http.Header{"X-Terraform-Get": {"/package.zip"}}, strings.Repeat("x", source.MaxModuleVanityDocumentBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), "GET", "https://up.example/module?private=query&terraform-get=1", nil)
			if err != nil {
				t.Fatal(err)
			}
			response := &http.Response{Request: req, StatusCode: tc.status, Header: tc.header, Body: io.NopCloser(strings.NewReader(tc.body)), ContentLength: int64(len(tc.body))}
			q := source.Request{Resource: source.Resource{ModuleVanityDocument: true}}
			err = remote.ProjectModuleResponse(q, response)
			defer response.Body.Close()
			if (err == nil) != tc.allowed {
				t.Fatal("vanity admission result", err)
			}
			if tc.allowed {
				var control source.ModuleVanityResponse
				if json.NewDecoder(response.Body).Decode(&control) != nil || len(control.Identity) != 64 || response.Header.Get("Content-Type") != "application/json" {
					t.Fatal("missing private projection")
				}
			}
		})
	}
	q := source.Request{Resource: source.Resource{ModuleVanityDocument: true}}
	key := (&remote.Policy{Key: []byte("test")}).CacheKey(q, "rev")
	q.Resource.ModuleVanityDocument = false
	if (&remote.Policy{Key: []byte("test")}).CacheKey(q, "rev") == key {
		t.Fatal("vanity operation was absent from cache identity")
	}
	q.Resource.ModuleVanityDocument = true
	request := remote.TransportRequest(q, model.RemoteCacheEntry{Blob: blob.Blob{Checksums: kernel.Checksums{SHA256: "digest"}}, ETag: `"upstream"`})
	if request.Header.Get("If-None-Match") != "" {
		t.Fatal("projected bytes sent upstream validators")
	}
}

type moduleVanityFailedReader struct{}

func (moduleVanityFailedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type moduleVanityCloseFailure struct{ io.Reader }

func (moduleVanityCloseFailure) Close() error { return errors.New("fixture close failed") }

func TestUnitModuleVanityProjectionRefusesIncompleteOrUnidentifiedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*http.Response)
	}{
		{"missing-request", func(r *http.Response) { r.Request = nil }},
		{"missing-url", func(r *http.Response) { r.Request.URL = nil }},
		{"invalid-url", func(r *http.Response) { r.Request.URL.Scheme = "file" }},
		{"malformed-query", func(r *http.Response) { r.Request.URL.RawQuery = "bad=%zz" }},
		{"header-whitespace", func(r *http.Response) { r.Header.Set("X-Terraform-Get", " /archive.zip") }},
		{"mixed-case-duplicates", func(r *http.Response) { r.Header["x-terraform-get"] = []string{"/other.zip"} }},
		{"read-error", func(r *http.Response) { r.Body = io.NopCloser(moduleVanityFailedReader{}); r.ContentLength = -1 }},
		{"length-mismatch", func(r *http.Response) { r.ContentLength++ }},
		{"close-error", func(r *http.Response) { r.Body = moduleVanityCloseFailure{strings.NewReader("body")} }},
		{"unknown-html-parameter", func(r *http.Response) {
			r.Header.Del("X-Terraform-Get")
			r.Header.Set("Content-Type", "text/html; encoding=unknown")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), "GET", "https://up.example/document", nil)
			if err != nil {
				t.Fatal(err)
			}
			response := &http.Response{
				Request:       request,
				StatusCode:    200,
				Header:        http.Header{"X-Terraform-Get": {"/archive.zip"}},
				Body:          io.NopCloser(strings.NewReader("body")),
				ContentLength: 4,
			}
			tc.change(response)
			q := source.Request{Resource: source.Resource{ModuleVanityDocument: true}}
			if err := remote.ProjectModuleResponse(q, response); err == nil {
				t.Fatal("incomplete or invalid response produced reusable metadata")
			}
			_ = response.Body.Close()
		})
	}
}

package source

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestUnitCocoaPodsGitDescriptor(t *testing.T) {
	valid := CocoaPodsGitSource{
		URL:        "https://github.com/org/project.git",
		Tag:        "1.0.0",
		Submodules: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CocoaPodsGitSource)
	}{
		{"scheme", func(g *CocoaPodsGitSource) { g.URL = "ssh://github.com/org/project.git" }},
		{"userinfo", func(g *CocoaPodsGitSource) { g.URL = "https://secret@github.com/org/project.git" }},
		{"query", func(g *CocoaPodsGitSource) { g.URL += "?token=secret" }},
		{"encoded path", func(g *CocoaPodsGitSource) { g.URL += "%2fescape" }},
		{"dot segment", func(g *CocoaPodsGitSource) { g.URL += "/../escape" }},
		{"empty path segment", func(g *CocoaPodsGitSource) { g.URL += "//x" }},
		{"invalid ref", func(g *CocoaPodsGitSource) { g.Tag = "bad..tag" }},
		{"ref lock", func(g *CocoaPodsGitSource) { g.Tag = "x.lock" }},
		{"control ref", func(g *CocoaPodsGitSource) { g.Branch = "a\nb" }},
		{"ref prefix", func(g *CocoaPodsGitSource) { g.Tag = ".hidden" }},
		{"empty ref part", func(g *CocoaPodsGitSource) { g.Tag = "a//b" }},
		{"short commit", func(g *CocoaPodsGitSource) { g.Commit = "abcdef" }},
		{"zero commit", func(g *CocoaPodsGitSource) { g.Commit = strings.Repeat("0", 40) }},
		{"invalid hex", func(g *CocoaPodsGitSource) { g.Commit = strings.Repeat("x", 40) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := valid
			tc.mutate(&g)
			if g.Validate() == nil {
				t.Fatalf("accepted %+v", g)
			}
		})
	}
	for _, change := range []CocoaPodsGitSource{{URL: valid.URL, Tag: "2"}, {URL: valid.URL, Branch: "main"}, {URL: valid.URL, Commit: strings.Repeat("a", 40)}, {URL: valid.URL, Tag: valid.Tag}} {
		if change.Validate() != nil || change.Variant() == valid.Variant() {
			t.Fatalf("unbound selector %+v", change)
		}
	}
	r := Resource{
		Kind:              ResourceKindArtifact,
		Method:            http.MethodGet,
		Path:              "sources/Pod/1/source.tar.gz",
		ExternalURL:       valid.URL,
		CocoaPodsGit:      &valid,
		Variant:           valid.Variant(),
		Mutable:           true,
		NoCrossOriginAuth: true,
		CheckURL:          func(context.Context, *url.URL) error { return nil },
		Validate:          func(context.Context, io.ReadSeeker, int64) error { return nil },
	}
	if err := validateCocoaPodsGitContract(r); err != nil {
		t.Fatal(err)
	}
	if err := validateCocoaPodsGitContract(Resource{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Resource)
	}{
		{"metadata", func(r *Resource) { r.Kind = ResourceKindMetadata }},
		{"post", func(r *Resource) { r.Method = http.MethodPost }},
		{"variant", func(r *Resource) { r.Variant = "forged" }},
		{"target", func(r *Resource) { r.ExternalURL = "https://other.invalid/repo.git" }},
		{"path", func(r *Resource) { r.Path = "source.zip" }},
		{"mutable", func(r *Resource) { r.Mutable = false }},
		{"validator", func(r *Resource) { r.Validate = nil }},
		{"guard", func(r *Resource) { r.CheckURL = nil }},
		{"credential", func(r *Resource) { r.NoCrossOriginAuth = false }},
		{"upstream", func(r *Resource) { r.UpstreamPath = "other" }},
		{"query", func(r *Resource) { r.Query = "x=y" }},
		{"body", func(r *Resource) { r.PostBody = []byte("body") }},
		{"transient", func(r *Resource) { r.Transient = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := r
			tc.mutate(&copy)
			if validateCocoaPodsGitContract(copy) == nil {
				t.Fatal("invalid descriptor contract accepted")
			}
		})
	}
}

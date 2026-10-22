package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	helmfmt "github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestRemoteAdmissionCancellationAndLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := repo.Config{URL: "https://upstream.example/charts/"}
	response := source.MetadataResponse{BaseURL: c.URL + "index.yaml"}
	body := bytes.NewReader([]byte("anything"))
	for name, err := range map[string]error{
		"index":      validateRemoteIndex(c)(ctx, body, body.Size(), response),
		"chart":      validateRemoteChart(helmfmt.ChartVersion{})(ctx, body, body.Size()),
		"provenance": validateRemoteProvenance(helmfmt.ChartVersion{}, "chart.tgz")(ctx, body, body.Size()),
	} {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s cancellation: %v", name, err)
		}
	}
	if _, err := (&chartContextReader{ctx: ctx, reader: body}).Read(
		make([]byte, 1),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if err := validateRemoteIndex(c)(t.Context(), body, remoteIndexLimit+1, response); err == nil {
		t.Fatal("oversized index accepted")
	}
	if err := validateRemoteProvenance(
		helmfmt.ChartVersion{},
		"chart.tgz",
	)(
		t.Context(),
		body,
		3<<20,
	); err == nil {
		t.Fatal("oversized provenance accepted")
	}
	if err := validateRemoteChart(helmfmt.ChartVersion{})(t.Context(), body, 0); err == nil {
		t.Fatal("empty chart accepted")
	}
	if err := validateRemoteChart(
		helmfmt.ChartVersion{},
	)(
		t.Context(),
		body,
		body.Size(),
	); err == nil {
		t.Fatal("non-gzip chart accepted")
	}
}

func TestRemoteMetadataSchemaAndAmbiguity(t *testing.T) {
	c := repo.Config{URL: "https://upstream.example/charts/"}
	for _, doc := range []string{
		"", "[]", "apiVersion: v1\nentries: []", "apiVersion: v1\nentries: {web: null}",
		"apiVersion: v1\nentries: {'../web': []}",
		"apiVersion: v1\nentries: {web: [{name: other, version: 1.0.0, urls: [web.tgz]}]}",
		"apiVersion: v1\nentries: {web: [{name: web, version: 1.0.0, urls: [web.tgz], digest: invalid}]}",
		"apiVersion: v1\nentries: {web: [{name: web, version: 1.0.0, urls: []}]}",
		"apiVersion: v1\nentries: {web: [{name: web, version: invalid, urls: [web.tgz]}]}",
		"apiVersion: v1\nentries: {web: [{name: web, version: 1.0.0, urls: [web.tgz]}, {name: web, version: 2.0.0, urls: [web.tgz]}]}",
	} {
		if _, err := decodeRemoteIndex(c, c.URL+"index.yaml", strings.NewReader(doc)); err == nil {
			t.Fatalf("invalid index accepted: %q", doc)
		}
	}
	for _, version := range []string{"1", "1.2", "v1.2.3", "01.002.3", "1.2.3-alpha.1+build"} {
		doc := fmt.Sprintf(
			"apiVersion: v1\nentries: {web: [{name: web, version: '%s', urls: [web.tgz]}]}",
			version,
		)
		index, err := decodeRemoteIndex(c, c.URL+"index.yaml", strings.NewReader(doc))
		if err != nil || index.versions["web"][0].chart.Version != version {
			t.Fatal("Helm version text not retained", version, err)
		}
	}
	for _, version := range []string{"", "1..0", "1.2.3.4", "x.1", "1.0.0-01", "1.0.0+"} {
		if remoteVersionValid(version) {
			t.Fatal("invalid semver", version)
		}
	}
	// A local relative path must not shadow the canonical route of a different
	// external destination, even when both entries claim the same chart identity.
	v := true
	c.ExternalDependenciesEnabled = &v
	c.ExternalDependenciesPatterns = []string{"https://external.example/**"}
	doc := "apiVersion: v1\nentries: {web: [{name: web, version: 1.0.0, urls: ['_external/https/external.example/web.tgz', 'https://external.example/web.tgz']}] }"
	if _, err := decodeRemoteIndex(c, c.URL+"index.yaml", strings.NewReader(doc)); err == nil {
		t.Fatal("ambiguous external route accepted")
	}
	if _, err := chartTarget(c, "", "web.tgz"); err == nil {
		t.Fatal("missing response context accepted")
	}
	if _, err := chartTarget(
		c,
		c.URL+"index.yaml",
		"http://upstream.example/charts/web.tgz",
	); err == nil {
		t.Fatal("TLS downgrade accepted")
	}
}

func TestRemoteDisclosureFencesEveryAuthority(t *testing.T) {
	for _, which := range []string{"prior", "metadata", "chart", "allowed"} {
		t.Run(which, func(t *testing.T) {
			called := map[string]bool{}
			check := func(name string) error {
				called[name] = true
				if name == which {
					return source.ErrDenied
				}
				return nil
			}
			result := source.Result{
				Revalidate: func(context.Context) error { return check("prior") },
			}
			metadata := source.Result{
				Revalidate: func(context.Context) error { return check("metadata") },
				CheckRead: func(_ context.Context, path string) error {
					if path != "web.tgz" {
						t.Fatal(path)
					}
					return check("chart")
				},
			}
			fenceHelmDisclosures(&result, metadata, []string{"web.tgz"})
			err := result.Revalidate(t.Context())
			if which == "allowed" {
				if err != nil || len(called) != 3 {
					t.Fatal("missing authority fence", called, err)
				}
			} else if !errors.Is(err, source.ErrDenied) {
				t.Fatal("revoked authority disclosed", which, err)
			}
		})
	}
}

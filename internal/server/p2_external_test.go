package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	p2ExternalComposite          = `<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="https://child.example/site"/></children></repository>`
	p2ExternalArtifactsComposite = `<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.artifact.repository.CompositeArtifactRepository"><children size="1"><child location="https://child.example/site"/></children></repository>`
)

func TestAccP2ExternalCompositeChildIsOwnedAndParentBound(t *testing.T) {
	parent := []byte(p2ExternalComposite)
	childFault := false
	var calls []source.Resource
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			calls = append(calls, q.Resource)
			var body []byte
			base := "https://root.example/site/" + q.Resource.Path
			switch {
			case q.Resource.Path == "compositeContent.xml":
				body = parent
			case q.Resource.Path == "compositeArtifacts.xml":
				body = []byte(p2ExternalArtifactsComposite)
			case strings.HasSuffix(q.Resource.Path, "/artifacts.xml.xz"):
				body = p2Fixture(t, "artifacts.xml.xz")
				base = q.Resource.ExternalURL
			case strings.HasSuffix(q.Resource.Path, "/content.jar"):
				if childFault {
					return source.Result{}, source.ErrUnavailable
				}
				body = p2Fixture(t, "content.jar")
				base = q.Resource.ExternalURL
			case strings.HasSuffix(q.Resource.Path, "/plugins/org.eclipse.glsp.layout.source_2.7.0.jar"):
				body = p2Fixture(t, "org.eclipse.glsp.layout.source_2.7.0.jar")
				base = q.Resource.ExternalURL
			default:
				return source.Result{}, store.ErrNotFound
			}
			if q.Resource.ValidateMetadata != nil {
				if err := q.Resource.ValidateMetadata(
					t.Context(),
					bytes.NewReader(body),
					int64(len(body)),
					source.MetadataResponse{BaseURL: base},
				); err != nil {
					t.Fatalf("metadata validation: %v", err)
				}
			} else if q.Resource.Validate != nil {
				if err := q.Resource.Validate(
					t.Context(),
					bytes.NewReader(body),
					int64(len(body)),
				); err != nil {
					t.Fatalf("artifact validation: %v", err)
				}
			}
			return source.Result{
				Metadata: source.MetadataResponse{BaseURL: base},
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	c := *repo.Defaults(&repo.Config{
		Key: "p2-external", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	root := w.do(http.MethodGet, "/artifactory/p2-external/compositeContent.xml", "admin", "")
	if root.Code != http.StatusOK {
		t.Fatalf("composite: %d %s", root.Code, root.Body)
	}
	if strings.Contains(root.Body.String(), "child.example") {
		t.Fatalf("external origin leaked to P2 client: %s", root.Body)
	}
	match := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(root.Body.String())
	if match == "" {
		t.Fatalf("child path missing: %s", root.Body)
	}
	child := w.do(http.MethodGet, "/artifactory/p2-external/"+match+"/content.jar", "admin", "")
	if child.Code != http.StatusOK ||
		!bytes.Equal(child.Body.Bytes(), p2Fixture(t, "content.jar")) {
		t.Fatalf("external child metadata: %d, %d bytes", child.Code, child.Body.Len())
	}
	if res := w.do(
		http.MethodGet,
		"/artifactory/p2-external/compositeArtifacts.xml",
		"admin",
		"",
	); res.Code != http.StatusOK {
		t.Fatalf("artifact composite: %d %s", res.Code, res.Body)
	}
	plugin := w.do(
		http.MethodGet,
		"/artifactory/p2-external/"+match+"/plugins/org.eclipse.glsp.layout.source_2.7.0.jar",
		"admin",
		"",
	)
	if plugin.Code != http.StatusOK ||
		!bytes.Equal(
			plugin.Body.Bytes(),
			p2Fixture(t, "org.eclipse.glsp.layout.source_2.7.0.jar"),
		) {
		t.Fatalf(
			"external child plugin: %d, %d bytes; calls: %+v",
			plugin.Code,
			plugin.Body.Len(),
			calls,
		)
	}
	var childReads int
	for _, call := range calls {
		if call.ExternalURL == "" {
			continue
		}
		childReads++
		if !strings.HasPrefix(call.ExternalURL, "https://child.example/site/") {
			t.Fatalf("wrong child target %s", call.ExternalURL)
		}
		if call.CheckURL == nil {
			t.Fatal("child target missing redirect origin guard")
		}
		bad, _ := url.Parse("https://other.example/site/content.jar")
		if call.CheckURL(t.Context(), bad) == nil {
			t.Fatal("cross-origin redirect admitted")
		}
		for _, raw := range []string{
			"https://user:secret@child.example/site/content.jar",
			"https://child.example/site/content.jar#fragment",
		} {
			candidate, _ := url.Parse(raw)
			if call.CheckURL(t.Context(), candidate) == nil {
				t.Fatalf("unsafe redirect admitted: %s", raw)
			}
		}
	}
	if childReads != 3 {
		t.Fatalf("child fetch count %d, want 3", childReads)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "p2-child-only", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
				Targets: map[string]types.Target{"p2-external": {Include: []string{"__p2_ext/**"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	beforeDenied := len(calls)
	denied := w.do(http.MethodGet, "/artifactory/p2-external/"+match+"/content.jar", "user", "")
	if denied.Code != http.StatusForbidden || len(calls) != beforeDenied {
		t.Fatalf(
			"child-only grant bypassed parent read: %d, calls %d",
			denied.Code,
			len(calls)-beforeDenied,
		)
	}
	forged := strings.Repeat("a", 64)
	if forged == strings.TrimPrefix(match, "__p2_ext/") {
		forged = strings.Repeat("b", 64)
	}
	before := len(calls)
	res := w.do(
		http.MethodGet,
		"/artifactory/p2-external/__p2_ext/"+forged+"/content.jar",
		"admin",
		"",
	)
	if res.Code == http.StatusOK || len(calls) == before ||
		strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf(
			"forged child path: status %d, calls %d, body %s",
			res.Code,
			len(calls)-before,
			res.Body,
		)
	}
	parent = []byte(
		strings.ReplaceAll(
			p2ExternalComposite,
			"https://child.example/site",
			"https://child.example/changed",
		),
	)
	res = w.do(http.MethodGet, "/artifactory/p2-external/"+match+"/content.jar", "admin", "")
	if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("revoked child still delivered: %d %s", res.Code, res.Body)
	}
	for _, call := range calls[before:] {
		if call.ExternalURL != "" {
			t.Fatalf("forged/revoked child fetched upstream: %s", call.ExternalURL)
		}
	}
	parent = []byte(p2ExternalComposite)
	childFault = true
	res = w.do(http.MethodGet, "/artifactory/p2-external/"+match+"/content.jar", "admin", "")
	if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("child upstream fault returned content or URL: %d %s", res.Code, res.Body)
	}
	childFault = false
	c.P2ChildOrigins = nil
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	before = len(calls)
	res = w.do(http.MethodGet, "/artifactory/p2-external/"+match+"/content.jar", "admin", "")
	if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("revoked origin still delivered child: %d %s", res.Code, res.Body)
	}
	for _, call := range calls[before:] {
		if call.ExternalURL != "" {
			t.Fatalf("revoked origin fetched upstream: %s", call.ExternalURL)
		}
	}
}

func TestAccP2NestedExternalCompositeAndCycleBound(t *testing.T) {
	composite := func(location string) []byte {
		return []byte(
			`<?xml version="1.0"?><repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository"><children size="1"><child location="` + location + `"/></children></repository>`,
		)
	}
	root := composite("https://a.example/site?revision=1")
	var externalReads int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			var body []byte
			base := "https://root.example/site/compositeContent.xml"
			switch {
			case q.Resource.Path == "compositeContent.xml":
				body = root
			case strings.HasSuffix(q.Resource.Path, "/compositeContent.xml") &&
				strings.HasPrefix(q.Resource.ExternalURL, "https://a.example/"):
				body = composite("https://b.example/site")
				base = q.Resource.ExternalURL
				externalReads++
			case strings.HasSuffix(q.Resource.Path, "/content.jar") &&
				strings.HasPrefix(q.Resource.ExternalURL, "https://b.example/"):
				body = p2Fixture(t, "content.jar")
				base = q.Resource.ExternalURL
				externalReads++
			default:
				return source.Result{}, store.ErrNotFound
			}
			return source.Result{
				Metadata: source.MetadataResponse{BaseURL: base},
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	c := *repo.Defaults(&repo.Config{
		Key: "p2-nested", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://a.example", "https://b.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-nested/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("root composite %d %s", res.Code, res.Body)
	}
	a := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	res = w.do(http.MethodGet, "/artifactory/p2-nested/"+a+"/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "b.example") {
		t.Fatalf("nested composite %d %s", res.Code, res.Body)
	}
	b := regexp.MustCompile(`[a-f0-9]{64}`).FindString(res.Body.String())
	res = w.do(http.MethodGet, "/artifactory/p2-nested/"+a+"/"+b+"/content.jar", "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), p2Fixture(t, "content.jar")) {
		t.Fatalf("nested child %d, %d bytes", res.Code, res.Body.Len())
	}
	if externalReads < 2 {
		t.Fatalf("external hops were not fetched: %d", externalReads)
	}
	root = composite("https://root.example/site/")
	res = w.do(http.MethodGet, "/artifactory/p2-nested/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("cycle parent %d %s", res.Code, res.Body)
	}
	cycle := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	before := externalReads
	res = w.do(http.MethodGet, "/artifactory/p2-nested/"+cycle+"/content.jar", "admin", "")
	if res.Code == http.StatusOK || externalReads != before {
		t.Fatalf("cycle delivered a child: %d, external reads %d", res.Code, externalReads-before)
	}
}

func TestAccP2ExternalCompositeRejectsDeniedOrigins(t *testing.T) {
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.ExternalURL != "" {
				t.Fatalf("disallowed child fetched: %s", q.Resource.ExternalURL)
			}
			body := []byte(p2ExternalComposite)
			return source.Result{
				Metadata: source.MetadataResponse{
					BaseURL: "https://root.example/site/compositeContent.xml",
				},
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	c := *repo.Defaults(&repo.Config{
		Key: "p2-denied", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/",
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-denied/compositeContent.xml", "admin", "")
	if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("disallowed origin leaked: %d %s", res.Code, res.Body)
	}
	if p2.Classify("__p2_ext/"+strings.Repeat("a", 64)+"/plugins/a.jar") != p2.Artifact {
		t.Fatal("synthetic plugin path not classified")
	}
}

func TestAccP2ArchiveCompositeRewritesExternalChild(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("compositeContent.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(p2ExternalComposite)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var childReads int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			var body []byte
			switch q.Resource.ExternalURL {
			case "https://root.example/site.zip":
				body = archive.Bytes()
			case "https://child.example/site/content.jar":
				body = p2Fixture(t, "content.jar")
				childReads++
			case "https://child.example/site/artifacts.xml.xz":
				body = p2Fixture(t, "artifacts.xml.xz")
			default:
				return source.Result{}, store.ErrNotFound
			}
			if q.Resource.Validate != nil {
				if err := q.Resource.Validate(
					t.Context(),
					bytes.NewReader(body),
					int64(len(body)),
				); err != nil {
					t.Fatalf("archive/child validation: %v", err)
				}
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	c := *repo.Defaults(&repo.Config{
		Key: "p2-zip-parent", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site.zip!/", P2ChildOrigins: []string{"https://child.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-zip-parent/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("archive composite leaked child: %d %s", res.Code, res.Body)
	}
	child := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	res = w.do(http.MethodGet, "/artifactory/p2-zip-parent/"+child+"/content.jar", "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), p2Fixture(t, "content.jar")) ||
		childReads != 1 {
		t.Fatalf(
			"archive external child: %d, %d bytes, child reads %d",
			res.Code,
			res.Body.Len(),
			childReads,
		)
	}
}

func TestAccP2VirtualDoesNotLendExternalChildAuthority(t *testing.T) {
	var externalReads int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			var body []byte
			switch {
			case q.Resource.Path == "compositeContent.xml":
				body = []byte(p2ExternalComposite)
			case q.Resource.ExternalURL == "https://child.example/site/artifacts.xml.xz":
				body = p2Fixture(t, "artifacts.xml.xz")
			case q.Resource.ExternalURL == "https://child.example/site/content.jar":
				body = p2Fixture(t, "content.jar")
				externalReads++
			default:
				return source.Result{}, store.ErrNotFound
			}
			return source.Result{
				Metadata: source.MetadataResponse{
					BaseURL: "https://root.example/site/compositeContent.xml",
				},
				Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			Resolve: func(_ context.Context, c repo.Config) (types.SourceIdentity, error) {
				return types.SourceIdentity{
					Kind:       types.ExternalHTTP,
					Repository: types.RepositoryIncarnation(c.Key), ID: c.Key,
					Generation: "v1",
				}, nil
			},
			HTTP: backend, Cache: backend,
		}
	})
	for _, c := range []repo.Config{
		{
			Key: "p2-allowed", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
		},
		{
			Key: "p2-restricted", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://root.example/site/",
		},
		{
			Key: "p2-members", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-allowed", "p2-restricted"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	res := w.do(http.MethodGet, "/artifactory/p2-members/__p2/0/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("allowed member composite: %d %s", res.Code, res.Body)
	}
	child := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	res = w.do(http.MethodGet, "/artifactory/p2-members/__p2/0/"+child+"/content.jar", "admin", "")
	if res.Code != http.StatusOK || externalReads != 1 {
		t.Fatalf("allowed member child: %d, reads %d", res.Code, externalReads)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-members/__p2/1/"+child+"/content.jar", "admin", "")
	if res.Code == http.StatusOK || externalReads != 1 ||
		strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf(
			"restricted member borrowed authority: %d, reads %d, %s",
			res.Code,
			externalReads,
			res.Body,
		)
	}
}

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccP2ExternalMetadataFallbackAndChecksumRequired(t *testing.T) {
	root := []byte(p2ExternalComposite)
	metadataMode := "fallback"
	var requested []string
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			requested = append(requested, q.Resource.Path)
			var body []byte
			switch {
			case q.Resource.Path == "compositeContent.xml":
				body = root
			case q.Resource.Path == "compositeArtifacts.xml":
				body = []byte(p2ExternalArtifactsComposite)
			case strings.HasSuffix(q.Resource.Path, "/artifacts.xml.xz"):
				if metadataMode == "malformed" {
					body = []byte("untrusted metadata")
				} else if metadataMode == "fault" {
					return source.Result{}, source.ErrUnavailable
				} else {
					return source.Result{}, store.ErrNotFound
				}
			case strings.HasSuffix(q.Resource.Path, "/artifacts.jar"):
				if metadataMode == "missing" {
					return source.Result{}, store.ErrNotFound
				}
				body = p2Fixture(t, "artifacts.jar")
			case strings.HasSuffix(q.Resource.Path, "/artifacts.xml"):
				return source.Result{}, store.ErrNotFound
			case strings.HasSuffix(q.Resource.Path, "/content.jar"):
				body = p2Fixture(t, "content.jar")
			case strings.HasSuffix(q.Resource.Path, "/plugins/org.eclipse.glsp.layout.source_2.7.0.jar"):
				body = p2Fixture(t, "org.eclipse.glsp.layout.source_2.7.0.jar")
			default:
				return source.Result{}, store.ErrNotFound
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
		Key: "p2-fallback", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-fallback/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
		t.Fatalf("root without response base leaked or failed: %d %s", res.Code, res.Body)
	}
	child := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	res = w.do(http.MethodGet, "/artifactory/p2-fallback/"+child+"/content.jar", "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), p2Fixture(t, "content.jar")) {
		t.Fatalf("child without response base failed: %d", res.Code)
	}
	pluginPath := "/artifactory/p2-fallback/" + child + "/plugins/org.eclipse.glsp.layout.source_2.7.0.jar"
	res = w.do(http.MethodGet, pluginPath, "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(),
		p2Fixture(t, "org.eclipse.glsp.layout.source_2.7.0.jar")) {
		t.Fatalf("published checksum JAR fallback failed: %d %s", res.Code, res.Body)
	}
	if !containsP2Request(requested, "/artifacts.xml.xz") ||
		!containsP2Request(requested, "/artifacts.jar") {
		t.Fatalf("published metadata fallback was not used: %v", requested)
	}
	for _, mode := range []string{"missing", "malformed", "fault"} {
		metadataMode = mode
		requested = nil
		res = w.do(http.MethodGet, pluginPath, "admin", "")
		if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") ||
			containsP2Request(requested, "/plugins/org.eclipse.glsp.layout.source_2.7.0.jar") {
			t.Fatalf("%s metadata delivered unpinned plugin: %d %s, requests %v",
				mode, res.Code, res.Body, requested)
		}
	}
}

func containsP2Request(requests []string, suffix string) bool {
	for _, path := range requests {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func TestUnitP2ReadFailsClosedAtSourceAndDeliveryFences(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "p2-read-errors", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	w := newItemWorld(t, false)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodGet,
		"/artifactory/p2-read-errors/compositeContent.xml",
		nil,
	)
	s := artifactoryService{}
	if _, err := s.p2Read(
		r,
		c,
		false,
		"compositeContent.xml",
		nil,
		"",
		p,
		acc,
	); !errors.Is(
		err,
		source.ErrUnavailable,
	) {
		t.Fatalf("missing source admitted: %v", err)
	}
	s.cfg.RemoteSources = &source.Service{
		Resolve: sourceIdentity, HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, source.ErrUnavailable
			}),
	}
	if _, err := s.p2Read(
		r,
		c,
		false,
		"compositeContent.xml",
		nil,
		"",
		p,
		acc,
	); !errors.Is(
		err,
		source.ErrUnavailable,
	) {
		t.Fatalf("source fault exposed content: %v", err)
	}
	if _, err := s.p2Read(r, c, false, "../secret", nil, "", p, acc); err == nil {
		t.Fatal("invalid metadata path reached source")
	}
	external, _ := url.Parse("https://child.example/site/content.jar")
	badSource := c
	badSource.URL = "://broken"
	if _, err := s.p2Read(
		r,
		badSource,
		false,
		"content.jar",
		external,
		"",
		p,
		acc,
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatalf("invalid source origin admitted for child: %v", err)
	}
	var checks int
	stream := &sourceBody{Reader: bytes.NewReader([]byte(p2ExternalComposite))}
	s.cfg.RemoteSources = &source.Service{
		Resolve: sourceIdentity,
		Revalidate: func(context.Context, source.Request, source.Permit) error {
			checks++
			if checks >= 3 {
				return source.ErrDenied
			}
			return nil
		},
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body: stream, Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(p2ExternalComposite))},
				}}, nil
			},
		),
	}
	if _, err := s.p2Read(
		r,
		c,
		false,
		"compositeContent.xml",
		nil,
		"",
		p,
		acc,
	); !errors.Is(err, source.ErrDenied) ||
		stream.closes.Load() == 0 {
		t.Fatalf("delivery fence admitted revoked read or leaked body: %v, closes %d",
			err, stream.closes.Load())
	}
}

func TestUnitP2ReadCachedMetadataUsesItsStoredBytes(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "p2-cached-read", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/",
	})
	w := newItemWorld(t, false)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(p2ExternalComposite)
	info := blobtest.Put(t.Context(), t, w.blobs, body)
	sha := info.Digests.SHA256
	backend := sourceBackend(
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			if permit.Intent != source.CacheOnly || permit.UpstreamNetworkAllowed {
				t.Fatalf("cache-only read used network permit: %+v", permit)
			}
			return source.Result{Item: &types.Item{
				RepoKey: c.Key, Path: types.RootPath, Name: q.Resource.Path,
				Cached: true, Actual: types.Checksums{SHA256: sha},
			}}, nil
		},
	)
	s := artifactoryService{cfg: Config{
		Blobs:         w.blobs,
		RemoteSources: &source.Service{Resolve: sourceIdentity, Cache: backend},
	}}
	r := httptest.NewRequest(
		http.MethodGet,
		"/artifactory/p2-cached-read/compositeContent.xml",
		nil,
	)
	result, err := s.p2Read(r, c, true, "compositeContent.xml", nil, "", p, acc)
	if err != nil || !bytes.Equal(result.body, body) {
		t.Fatalf("stored metadata not delivered offline: %v, %d bytes", err, len(result.body))
	}
	sha = strings.Repeat("0", 64)
	if _, err := s.p2Read(r, c, true, "compositeContent.xml", nil, "", p, acc); err == nil {
		t.Fatal("missing cached blob was delivered")
	}
}

func TestUnitP2ReadBoundsStreamAndExtractsOnlyValidArchives(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "p2-bounded-read", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	w := newItemWorld(t, false)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/artifactory/p2-bounded-read/content.jar", nil)
	longMetadata := bytes.Repeat([]byte("x"), p2.MaxMetadata+1)
	streamBody := longMetadata
	s := artifactoryService{cfg: Config{RemoteSources: &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(streamBody)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(streamBody))},
				}}, nil
			},
		),
	}}}
	if _, err := s.p2Read(
		r,
		c,
		false,
		"content.jar",
		nil,
		"",
		p,
		acc,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("oversized metadata stream admitted: %v", err)
	}
	streamBody = []byte("not a ZIP archive")
	archive, _ := url.Parse("https://child.example/site.zip")
	if _, err := s.p2Read(r, c, false, "__p2_ext/opaque/content.jar", archive,
		"content.jar", p, acc); !errors.Is(err, source.ErrInvalidResult) {
		t.Fatalf("invalid child archive entry delivered: %v", err)
	}
}

func TestUnitP2ArchiveEntryValidationFailsBeforePublication(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "p2-archive-errors", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	w := newItemWorld(t, false)
	p := auth.Principal{Name: "admin", Admin: true}
	acc, err := accessFor(t.Context(), w.db, p)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodGet,
		"/artifactory/p2-archive-errors/compositeContent.xml",
		nil,
	)
	target, _ := url.Parse("https://child.example/site.zip")
	for _, tc := range []struct {
		name string
		body []byte
		size int64
	}{
		{"empty", nil, 0},
		{"truncated", []byte("bad archive"), 1},
		{"not a ZIP", []byte("bad archive"), int64(len("bad archive"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := artifactoryService{cfg: Config{RemoteSources: &source.Service{
				Resolve: sourceIdentity,
				HTTP: sourceBackend(
					func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
						if q.Resource.Validate == nil {
							t.Fatal("archive validator missing")
						}
						if err := q.Resource.Validate(
							t.Context(),
							bytes.NewReader(tc.body),
							tc.size,
						); err != nil {
							return source.Result{}, err
						}
						return source.Result{}, nil
					},
				),
			}}}
			if _, err := s.p2Read(r, c, false, "__p2_ext/opaque/compositeContent.xml",
				target, "compositeContent.xml", p, acc); err == nil {
				t.Fatal("invalid archive entry admitted")
			}
		})
	}
}

func TestAccP2ArchiveCorruptionStaysBehindRepositoryRoute(t *testing.T) {
	for _, mode := range []string{"source missing", "source denied", "empty size", "wrong size", "invalid validator", "stream fault", "invalid delivery"} {
		t.Run(mode, func(t *testing.T) {
			var fetched int
			w := newItemWorld(t, false, func(cfg *Config) {
				if mode == "source missing" {
					return
				}
				cfg.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
					func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
						fetched++
						if mode == "source denied" {
							return source.Result{}, source.ErrDenied
						}
						if mode == "stream fault" {
							return source.Result{Stream: &source.Stream{
								Body: io.NopCloser(p2BrokenReader{}), Status: http.StatusOK,
								Representation: source.Representation{Size: -1},
							}}, nil
						}
						body := []byte("not a ZIP archive")
						if mode == "invalid validator" || mode == "empty size" ||
							mode == "wrong size" {
							size := int64(len(body))
							if mode == "empty size" {
								size = 0
							} else if mode == "wrong size" {
								size--
							}
							if q.Resource.Validate == nil || q.Resource.Validate(t.Context(),
								bytes.NewReader(body), size) == nil {
								t.Fatal("malformed upstream archive passed source validation")
							}
							return source.Result{}, source.ErrInvalidResult
						}
						return source.Result{Stream: &source.Stream{
							Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
							Representation: source.Representation{Size: int64(len(body))},
						}}, nil
					},
				), Cache: sourceBackend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, store.ErrNotFound
				})}
			})
			c := *repo.Defaults(&repo.Config{
				Key: "p2-bad-zip", Class: repo.Remote, PackageType: repo.P2,
				URL: "https://root.example/site.zip!/",
			})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			res := w.do(http.MethodGet, "/artifactory/p2-bad-zip/p2.index", "admin", "")
			if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "root.example") {
				t.Fatalf("%s exposed archive or source URL: %d %s", mode, res.Code, res.Body)
			}
			if mode == "source missing" && fetched != 0 {
				t.Fatal("missing source still fetched upstream")
			}
		})
	}
}

type p2BrokenReader struct{}

func (p2BrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAccP2VirtualRejectsForgedMemberSelectionAndCycle(t *testing.T) {
	var childReads int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "content.jar" {
				return source.Result{}, store.ErrNotFound
			}
			childReads++
			body := p2Fixture(t, "content.jar")
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	for _, c := range []repo.Config{
		{
			Key: "p2-safe-member", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://root.example/site/",
		},
		{
			Key: "p2-safe-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-safe-member"},
		},
		{
			Key: "p2-cycle-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-cycle-virtual"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{
		"__p2/0", "__p2/x/content.jar", "__p2/-1/content.jar",
		"__p2/01/content.jar", "__p2/1/content.jar", "__p2/0/unknown.txt",
	} {
		res := w.do(http.MethodGet, "/artifactory/p2-safe-virtual/"+bad, "admin", "")
		if res.Code == http.StatusOK || childReads != 0 {
			t.Fatalf(
				"forged virtual member %s reached source: %d, reads %d",
				bad,
				res.Code,
				childReads,
			)
		}
	}
	res := w.do(http.MethodGet, "/artifactory/p2-safe-virtual/__p2/0/content.jar", "admin", "")
	if res.Code != http.StatusOK || childReads != 1 ||
		!bytes.Equal(res.Body.Bytes(), p2Fixture(t, "content.jar")) {
		t.Fatalf("valid member route changed: %d, reads %d", res.Code, childReads)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-cycle-virtual/compositeContent.xml", "admin", "")
	if res.Code == http.StatusOK || childReads != 1 {
		t.Fatalf("virtual member cycle leaked content: %d, reads %d", res.Code, childReads)
	}
}

func TestAccP2ExternalCompositeDepthStopsBeforePublishingNextChild(t *testing.T) {
	composite := func(next int) []byte {
		return []byte(strings.Replace(p2ExternalComposite,
			"https://child.example/site", fmt.Sprintf("https://child.example/site%d", next), 1))
	}
	var highestFetched int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			var body []byte
			base := "https://root.example/site/compositeContent.xml"
			if q.Resource.Path == "compositeContent.xml" {
				body = composite(1)
			} else if q.Resource.ExternalURL != "" {
				var at int
				if _, err := fmt.Sscanf(
					q.Resource.ExternalURL,
					"https://child.example/site%d/compositeContent.xml",
					&at,
				); err != nil || at < 1 {
					return source.Result{}, store.ErrNotFound
				}
				if at > highestFetched {
					highestFetched = at
				}
				body = composite(at + 1)
				base = q.Resource.ExternalURL
			} else {
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
		Key: "p2-depth", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-depth/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("root composite: %d %s", res.Code, res.Body)
	}
	path := regexp.MustCompile(`__p2_ext/[a-f0-9]{64}`).FindString(res.Body.String())
	for depth := 1; depth <= p2.MaxDepth; depth++ {
		res = w.do(
			http.MethodGet,
			"/artifactory/p2-depth/"+path+"/compositeContent.xml",
			"admin",
			"",
		)
		if depth == p2.MaxDepth {
			if res.Code == http.StatusOK || strings.Contains(res.Body.String(), "child.example") ||
				highestFetched != p2.MaxDepth {
				t.Fatalf("depth bound exposed next origin: %d, fetched %d, %s",
					res.Code, highestFetched, res.Body)
			}
			break
		}
		if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "child.example") {
			t.Fatalf("composite depth %d failed or leaked: %d %s", depth, res.Code, res.Body)
		}
		next := regexp.MustCompile(`[a-f0-9]{64}`).FindString(res.Body.String())
		if next == "" {
			t.Fatalf("composite depth %d missing child token: %s", depth, res.Body)
		}
		path += "/" + next
	}
}

func TestAccP2VirtualInventoryKeepsOnlyReadableNativeMembersInOrder(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{
			Key: "p2-inventory-remote", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://root.example/site/",
		},
		{
			Key: "p2-inventory-npm", Class: repo.Remote, PackageType: repo.NPM,
			URL: "https://npm.example/",
		},
		{Key: "p2-inventory-local", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "p2-inventory-nested", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-inventory-remote"},
		},
		{
			Key: "p2-inventory-root", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{
				"p2-inventory-missing", "p2-inventory-npm",
				"p2-inventory-remote", "p2-inventory-nested", "p2-inventory-local",
			},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	res := w.do(http.MethodGet, "/artifactory/p2-inventory-root/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `children size="3"`) ||
		!strings.Contains(res.Body.String(), "__p2/0") ||
		!strings.Contains(res.Body.String(), "__p2/1") ||
		!strings.Contains(res.Body.String(), "__p2/2") ||
		strings.Contains(res.Body.String(), "__p2/3") {
		t.Fatalf("virtual native member order/filter changed: %d %s", res.Code, res.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "p2-inventory-virtual-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"p2-inventory-root": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-inventory-root/compositeContent.xml", "user", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `children size="0"`) ||
		strings.Contains(res.Body.String(), "__p2/") {
		t.Fatalf("virtual-only grant disclosed member inventory: %d %s", res.Code, res.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "p2-inventory-narrow-member",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"p2-inventory-remote": {Include: []string{"foo/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-inventory-root/compositeContent.xml", "user", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `children size="1"`) {
		t.Fatalf("narrow member not traversable in virtual inventory: %d %s", res.Code, res.Body)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-inventory-root/__p2/0/content.jar", "user", "")
	if res.Code != http.StatusForbidden {
		t.Fatalf("virtual path bypassed member file denial: %d %s", res.Code, res.Body)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-inventory-root/content.jar", "admin", "")
	if res.Code == http.StatusOK {
		t.Fatal("virtual repository served member metadata without its member route")
	}
}

func TestAccP2ArchiveSuffixReadsStoredCacheOffline(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("p2.index")
	if err != nil {
		t.Fatal(err)
	}
	index := p2Fixture(t, "p2.index")
	if _, err := entry.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var used int
	var cacheSHA string
	w := newItemWorld(t, false, func(cfg *Config) {
		cfg.RemoteSources = &source.Service{
			Resolve: sourceIdentity,
			Cache: sourceBackend(
				func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
					if permit.Intent != source.CacheOnly || permit.UpstreamNetworkAllowed ||
						q.Resource.ExternalURL != "https://root.example/site.zip" {
						t.Fatalf("offline archive read escaped cache: %+v, %+v", permit, q.Resource)
					}
					return source.Result{
						Item: &types.Item{
							RepoKey: q.Repo.Key,
							Path:    types.RootPath,
							Name:    q.Resource.Path,
							Cached:  true,
							Actual:  types.Checksums{SHA256: cacheSHA},
						},
						Used: func(context.Context) error { used++; return nil },
					}, nil
				},
			),
		}
	})
	cacheSHA = blobtest.Put(t.Context(), t, w.blobs, archive.Bytes()).Digests.SHA256
	c := *repo.Defaults(&repo.Config{
		Key: "p2-offline-zip", Class: repo.Remote,
		PackageType: repo.P2, URL: "https://root.example/site.zip!/",
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-offline-zip-cache/p2.index", "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), index) || used != 1 {
		t.Fatalf("offline archive cache delivery: %d, used %d, %s", res.Code, used, res.Body)
	}
	cacheSHA = strings.Repeat("0", 64)
	res = w.do(http.MethodGet, "/artifactory/p2-offline-zip-cache/p2.index", "admin", "")
	if res.Code == http.StatusOK || used != 1 {
		t.Fatalf("missing cached archive blob was delivered: %d, used %d", res.Code, used)
	}
}

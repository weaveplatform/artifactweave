package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/p2"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/blobtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitP2ReadFailsClosedAtSourceAndDeliveryFences(t *testing.T) {
	c := *repo.Defaults(&repo.Config{
		Key: "p2-read-errors", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://root.example/site/", P2ChildOrigins: []string{"https://child.example"},
	})
	w := newItemWorld(t, false)
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(
		http.MethodGet,
		"/artifactory/p2-read-errors/compositeContent.xml",
		nil,
	)
	s := p2Items{newKit(Config{})}
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
	s.RemoteSources = &source.Service{
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
	s.RemoteSources = &source.Service{
		Resolve: sourceIdentity, HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, nil
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
		source.ErrInvalidResult,
	) {
		t.Fatalf("source response without bytes was accepted: %v", err)
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
	if _, err := s.p2ResolveExternal(
		r,
		badSource,
		false,
		"__p2_ext/"+strings.Repeat("a", 64)+"/content.jar",
		p,
		acc,
	); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("invalid source origin admitted while resolving child: %v", err)
	}
	var checks int
	stream := &sourceBody{Reader: bytes.NewReader([]byte(p2ExternalComposite))}
	s.RemoteSources = &source.Service{
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
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(p2ExternalComposite)
	info := blobtest.Put(t.Context(), t, w.blobs, body)
	sha := info.Digests.SHA256
	backend := sourceBackend(
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			if permit.Intent != source.RetrievalIntentCacheOnly || permit.UpstreamNetworkAllowed {
				t.Fatalf("cache-only read used network permit: %+v", permit)
			}
			return source.Result{Item: &types.Item{
				RepoKey: c.Key, Path: types.RootPath, Name: q.Resource.Path,
				Cached: true, Actual: types.Checksums{SHA256: sha},
			}}, nil
		},
	)
	s := p2Items{newKit(Config{
		Blobs:         w.blobs,
		RemoteSources: &source.Service{Resolve: sourceIdentity, Cache: backend},
	})}
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
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/artifactory/p2-bounded-read/content.jar", nil)
	longMetadata := bytes.Repeat([]byte("x"), p2.MaxMetadata+1)
	streamBody := longMetadata
	s := p2Items{newKit(Config{RemoteSources: &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(streamBody)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(streamBody))},
				}}, nil
			},
		),
	}})}
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
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
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
		{"not a zip", []byte("bad archive"), int64(len("bad archive"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := p2Items{newKit(Config{RemoteSources: &source.Service{
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
			}})}
			if _, err := s.p2Read(r, c, false, "__p2_ext/opaque/compositeContent.xml",
				target, "compositeContent.xml", p, acc); err == nil {
				t.Fatal("invalid archive entry admitted")
			}
		})
	}
}

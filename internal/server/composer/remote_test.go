package composer

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Composer's dist.shasum is SHA-1 on the wire.
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type sourceBackend struct {
	content       map[string][]byte
	calls         []string
	readError     map[string]error
	revalidateErr error
	revalidateAt  int
	revalidations int
}
type readCloser struct{ *bytes.Reader }

func (*readCloser) Close() error { return nil }

func (b *sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	b.calls = append(b.calls, fmt.Sprintf("%v:%s", p.Intent, q.Resource.Path))
	if err := b.readError[q.Resource.Path]; err != nil {
		return source.Result{}, err
	}
	data, ok := b.content[q.Resource.Path]
	if !ok {
		return source.Result{}, store.ErrNotFound
	}
	base := q.Resource.ExternalURL
	reader := bytes.NewReader(data)
	if q.Resource.ValidateMetadata != nil {
		if err := q.Resource.ValidateMetadata(
			ctx,
			reader,
			int64(len(data)),
			source.MetadataResponse{BaseURL: base},
		); err != nil {
			return source.Result{}, source.ErrInvalidResult
		}
		_, _ = reader.Seek(0, io.SeekStart)
	}
	if q.Resource.Validate != nil {
		if err := q.Resource.Validate(ctx, reader, int64(len(data))); err != nil {
			return source.Result{}, source.ErrInvalidResult
		}
		_, _ = reader.Seek(0, io.SeekStart)
	}
	if q.Resource.Checksums.SHA1 != "" {
		digest := sha1.Sum(data) //nolint:gosec // test mirrors Composer's declared digest.
		if hex.EncodeToString(digest[:]) != q.Resource.Checksums.SHA1 {
			return source.Result{}, source.ErrInvalidResult
		}
	}
	return source.Result{
		Stream: &source.Stream{
			Body:   &readCloser{reader},
			Status: http.StatusOK,
			Representation: source.Representation{
				Size:      int64(len(data)),
				MediaType: "application/json",
			},
		},
		Metadata: source.MetadataResponse{BaseURL: base},
		Revalidate: func(context.Context) error {
			b.revalidations++
			if b.revalidateAt != 0 && b.revalidations == b.revalidateAt {
				return source.ErrUnavailable
			}
			return b.revalidateErr
		},
	}, nil
}

func TestAccRemoteComposerMetadataCacheAndIntegrity(t *testing.T) {
	archive := makeZIP(t, "1.0.0", "remote")
	digest := sha1.Sum(archive) //nolint:gosec // Composer's dist.shasum.
	backend := &sourceBackend{content: map[string][]byte{
		"packages.json": []byte(`{"packages":{},"metadata-url":"/p2/%package%.json"}`),
		"p2/acme/widget.json": []byte(
			fmt.Sprintf(
				`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip","shasum":"%x"}}]}}`,
				digest,
			),
		),
		"acme/widget/1.0.0.zip": archive,
	}}
	svc := &source.Service{
		HTTP:  backend,
		Cache: backend,
		Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{
				Kind:       types.ExternalHTTP,
				Repository: "r1",
				ID:         "s1",
				Generation: "g1",
			}, nil
		},
	}
	f := newFixtureWithSource(t, svc)
	c := f.create("remote", repo.Remote)
	c.URL, c.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/composer/remote"
	if got := f.request(
		"GET",
		base+"/packages.json",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), "metadata-url") {
		t.Fatalf("root %d %s", got.Code, got.Body)
	}
	if got := f.request(
		"GET",
		base+"/p2/acme/widget.json",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), base+"/dist/acme/widget/1.0.0.zip") {
		t.Fatalf("p2 %d %s", got.Code, got.Body)
	}
	if got := f.request(
		"GET",
		base+"/dist/acme/widget/1.0.0.zip",
	); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), archive) {
		t.Fatalf("dist %d %s", got.Code, got.Body)
	}
	before := len(backend.calls)
	if got := f.request(
		"GET",
		"/artifactory/api/composer/remote-cache/p2/acme/widget.json",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), "remote-cache/dist") {
		t.Fatalf("cache p2 %d %s", got.Code, got.Body)
	}
	if len(backend.calls) <= before ||
		!strings.Contains(strings.Join(backend.calls[before:], ","), "1:packages.json") {
		t.Fatalf("cache intent not recorded: %v", backend.calls[before:])
	}
	backend.content["acme/widget/1.0.0.zip"] = makeZIP(t, "1.0.0", "tampered")
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code == 200 {
		t.Fatal("checksum mismatch served")
	}
	backend.content["p2/acme/widget.json"] = []byte(`{"packages":{"other/widget":[]}}`)
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code == 200 {
		t.Fatal("wrong package metadata served")
	}
}

func TestUnitRemoteComposerRejectsUntrustedMetadata(t *testing.T) {
	for _, doc := range []string{
		`{}`, `{"metadata-url":"/p2/fixed.json"}`, `{"metadata-url":"https://evil.example/p2/%package%.json"}`,
		`{"metadata-url":"/p2/%package%.json?token=secret"}`,
	} {
		if _, err := parseRoot([]byte(doc), "https://up.example/packages.json"); err == nil {
			t.Errorf("accepted root %s", doc)
		}
	}
	for _, doc := range []string{
		`{"packages":{"other/widget":[]}}`,
		`{"packages":{"acme/widget":[{"name":"other/widget","version":"1.0","dist":{"type":"zip","url":"https://up.example/a.zip"}}]}}`,
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0","dist":{"type":"zip","url":"http://user:pass@up.example/a.zip"}}]}}`,
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0","dist":{"type":"zip","url":"https://up.example/a.zip","shasum":"bad"}}]}}`,
	} {
		if _, err := parseP2Doc(
			[]byte(doc),
			"acme/widget",
			"https://up.example/p2/acme/widget.json",
		); err == nil {
			t.Errorf("accepted p2 %s", doc)
		}
	}
}

func TestUnitRemoteComposerValidationEdges(t *testing.T) {
	if got := registryURL("https://up.example/", ""); got != "https://up.example" {
		t.Fatal(got)
	}
	if _, err := readMetadata(source.Result{}); err == nil {
		t.Fatal("missing body accepted")
	}
	if _, err := readMetadata(
		source.Result{
			Stream: &source.Stream{
				Body: io.NopCloser(strings.NewReader(strings.Repeat("x", remoteMetadataLimit+1))),
			},
		},
	); err == nil {
		t.Fatal("oversize body accepted")
	}
	for _, tc := range []struct{ body, base string }{
		{"not json", "https://up.example/packages.json"},
		{`{"metadata-url":"/%ZZ/%package%.json"}`, "https://up.example/packages.json"},
		{`{"metadata-url":"/p2/%package%.json"}`, "broken base"},
		{`{"metadata-url":"https://up.example/p2/%package%.json#fragment"}`, "https://up.example/packages.json"},
		{`{"metadata-url":"/p2/%package%/%package%.json"}`, "https://up.example/packages.json"},
	} {
		if _, err := parseRoot([]byte(tc.body), tc.base); err == nil {
			t.Errorf("accepted root %s base %s", tc.body, tc.base)
		}
	}
	if got, err := parseRoot(
		[]byte(`{"metadata-url":"/p2/%package%.json"}`),
		"https://up.example/packages.json",
	); err != nil ||
		got != "https://up.example/p2/%package%.json" {
		t.Fatalf("template %s %v", got, err)
	}
	for _, href := range []string{"::bad", "file:///etc/passwd", "https://user:pass@up.example/a.zip", "https://up.example/a.zip?token=secret", "https://up.example/a.zip#fragment"} {
		if _, err := resolveDist("https://up.example/p2/acme/widget.json", href); err == nil {
			t.Errorf("accepted dist %s", href)
		}
	}
	if _, err := resolveDist("https://up.example/%ZZ", "dist.zip"); err == nil {
		t.Fatal("invalid base URL accepted")
	}
	if got, err := resolveDist(
		"https://up.example/p2/acme/widget.json",
		"../../dist.zip",
	); err != nil ||
		got != "https://up.example/dist.zip" {
		t.Fatalf("relative dist %s %v", got, err)
	}
	for _, doc := range []string{
		`{"packages":{"acme/widget":[null]}}`,
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0","dist":{"type":"tar","url":"https://up.example/a.zip"}}]}}`,
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0","dist":{"type":"zip","url":"https://up.example/a.zip"}},{"name":"acme/widget","version":"1.0","dist":{"type":"zip","url":"https://up.example/b.zip"}}]}}`,
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"a/b","dist":{"type":"zip","url":"https://up.example/a.zip"}}]}}`,
	} {
		if _, err := parseP2Doc(
			[]byte(doc),
			"acme/widget",
			"https://up.example/p2/acme/widget.json",
		); err == nil {
			t.Errorf("accepted p2 %s", doc)
		}
	}
}

func TestAccRemoteComposerRevokedAtDelivery(t *testing.T) {
	for _, endpoint := range []string{"p2/acme/widget.json", "dist/acme/widget/1.0.0.zip"} {
		t.Run(endpoint, func(t *testing.T) {
			archive := makeZIP(t, "1.0.0", "revoked")
			b := &sourceBackend{content: map[string][]byte{
				"packages.json": []byte(`{"metadata-url":"/p2/%package%.json"}`),
				"p2/acme/widget.json": []byte(
					`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip"}}]}}`,
				),
				"acme/widget/1.0.0.zip": archive,
			}, revalidateAt: 5}
			svc := &source.Service{
				HTTP:  b,
				Cache: b,
				Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
					return types.SourceIdentity{
						Kind:       types.ExternalHTTP,
						Repository: "remote",
						ID:         "source",
						Generation: "1",
					}, nil
				},
			}
			f := newFixtureWithSource(t, svc)
			r := f.create("remote", repo.Remote)
			r.URL, r.ComposerRegistryURL = "https://up.example", "https://up.example"
			if err := f.db.Repos().Update(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if got := f.request(
				"GET",
				"/artifactory/api/composer/remote/"+endpoint,
			); got.Code == 200 {
				t.Fatalf("revoked source metadata served: %s", got.Body)
			}
		})
	}
}

func TestAccRemoteComposerWithoutRegistryURL(t *testing.T) {
	f := newFixtureWithSource(t, &source.Service{})
	f.create("remote", repo.Remote)
	if got := f.request("GET", "/artifactory/api/composer/remote/packages.json"); got.Code == 200 {
		t.Fatalf("unconfigured remote returned a catalog: %s", got.Body)
	}
}

func TestAccRemoteComposerSourceFailures(t *testing.T) {
	b := &sourceBackend{
		content: map[string][]byte{
			"packages.json": []byte(`{"metadata-url":"/p2/%package%.json"}`),
		},
	}
	denyDist := false
	svc := &source.Service{
		HTTP:  b,
		Cache: b,
		Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{
				Kind:       types.ExternalHTTP,
				Repository: "r1",
				ID:         "s1",
				Generation: "g1",
			}, nil
		},
		Revalidate: func(_ context.Context, q source.Request, _ source.Permit) error {
			if denyDist && q.Resource.Path == "acme/widget/1.0.0.zip" {
				return source.ErrDenied
			}
			return nil
		},
	}
	f := newFixtureWithSource(t, svc)
	c := f.create("remote", repo.Remote)
	c.URL, c.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/composer/remote"
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 404 {
		t.Fatalf("missing p2 %d %s", got.Code, got.Body)
	}
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code != 404 {
		t.Fatalf("missing dist %d", got.Code)
	}
	b.content["packages.json"] = []byte(`{"metadata-url":"https://evil.example/p2/%package%.json"}`)
	if got := f.request("GET", base+"/packages.json"); got.Code == 200 {
		t.Fatal("malicious root served")
	}
	b.content["packages.json"] = []byte(`{"metadata-url":"/p2/%package%.json"}`)
	b.content["p2/acme/widget.json"] = []byte(
		`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip"}}]}}`,
	)
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 200 {
		t.Fatalf("valid p2 %d %s", got.Code, got.Body)
	}
	denyDist = true
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code != 404 {
		t.Fatalf("disclosed archive permission: %d %s", got.Code, got.Body)
	}
	denyDist = false
	b.revalidateErr = source.ErrUnavailable
	if got := f.request("GET", base+"/packages.json"); got.Code == 200 {
		t.Fatal("stale metadata authority served")
	}
	b.revalidateErr = nil
	b.readError = map[string]error{"p2/acme/widget.json": source.ErrUnavailable}
	if got := f.request("GET", base+"/p2/acme/widget.json"); got.Code == 200 {
		t.Fatal("failed upstream metadata served")
	}
	b.readError = nil
	if got := f.request("GET", base+"/dist/acme/widget/1.0.0.zip"); got.Code == 200 {
		t.Fatal("absent archive served")
	}
}

func TestAccComposerVirtualSelectsRemoteDistribution(t *testing.T) {
	archive := makeZIP(t, "1.0.0", "remote first")
	digest := sha1.Sum(archive) //nolint:gosec // Composer's wire checksum.
	b := &sourceBackend{content: map[string][]byte{
		"packages.json": []byte(`{"metadata-url":"/p2/%package%.json"}`),
		"p2/acme/widget.json": []byte(
			fmt.Sprintf(
				`{"packages":{"acme/widget":[{"name":"acme/widget","version":"1.0.0","dist":{"type":"zip","url":"https://up.example/dist.zip","shasum":"%x"}}]}}`,
				digest,
			),
		),
		"acme/widget/1.0.0.zip": archive,
	}}
	svc := &source.Service{
		HTTP:  b,
		Cache: b,
		Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) {
			return types.SourceIdentity{
				Kind:       types.ExternalHTTP,
				Repository: "remote",
				ID:         "source",
				Generation: "1",
			}, nil
		},
	}
	f := newFixtureWithSource(t, svc)
	r := f.create("remote", repo.Remote)
	r.URL, r.ComposerRegistryURL = "https://up.example", "https://up.example"
	if err := f.db.Repos().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	local := f.create("local", repo.Local)
	f.deploy(local, "1.0.0", "local second")
	f.create("all", repo.Virtual, "remote", "local")
	base := "/artifactory/api/composer/all"
	if got := f.request(
		"GET",
		base+"/p2/acme/widget.json",
	); got.Code != 200 ||
		!strings.Contains(got.Body.String(), base+"/dist/acme/widget/1.0.0.zip") {
		t.Fatalf("virtual remote metadata: %d %s", got.Code, got.Body)
	}
	if got := f.request(
		"GET",
		base+"/dist/acme/widget/1.0.0.zip",
	); got.Code != 200 ||
		!bytes.Equal(got.Body.Bytes(), archive) {
		t.Fatalf("virtual remote distribution: %d %s", got.Code, got.Body)
	}
}

package remote

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	goformat "github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	goModule        = "example.com/Team/Mod"
	goEscapedModule = "example.com/!team/!mod"
)

func goArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, file := range []struct{ name, body string }{
		{"go.mod", "module " + name + "\n\ngo 1.23\n"},
		{"mod.go", "package mod\nconst Version = 1\n"},
	} {
		w, err := z.Create(name + "@" + version + "/" + file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func goSourceFixture(t *testing.T) (*policyWorld, GoSource, source.Request) {
	t.Helper()
	w := policyFixture(t)
	w.c.PackageType = repo.Go
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	subject := authz.Subject{Name: "reader", Admin: true}
	access, err := authz.New(
		t.Context(),
		authz.Sources{Permissions: w.db.Permissions(), Groups: w.db.Groups()},
		subject,
	)
	if err != nil {
		t.Fatal(err)
	}
	s := &source.Service{
		Resolve: func(ctx context.Context, c repo.Config) (types.SourceIdentity, error) {
			return w.db.RemoteCache().Source(ctx, c.Key)
		},
		HTTP: w.p, Cache: w.p,
	}
	return w, GoSource{Source: s}, source.Request{Repo: w.c, Subject: subject, Access: access}
}

func readGo(
	t *testing.T,
	w *policyWorld,
	src GoSource,
	q source.Request,
	path string,
	want []byte,
) source.Result {
	t.Helper()
	q.Resource = source.Resource{Path: path, Method: http.MethodGet}
	out, err := src.Read(t.Context(), q)
	if err != nil {
		t.Fatal(path, err)
	}
	if got := w.bytes(out); got != string(want) {
		t.Fatalf("%s bytes changed", path)
	}
	if out.Revalidate == nil || out.Revalidate(t.Context()) != nil {
		t.Fatal("missing source delivery fence")
	}
	if out.Item != nil && out.Item.Actual.SHA256 != fmt.Sprintf("%x", sha256.Sum256(want)) {
		t.Fatal("stored digest differs from upstream bytes")
	}
	return out
}

func TestGoSourceEndpointsEscapingAndOffline(t *testing.T) {
	w, src, q := goSourceFixture(t)
	version := "v1.2.3"
	fixtures := map[string][]byte{
		"/@v/list": []byte("v1.2.3\nv1.3.0\n"),
		"/@latest": []byte(
			`{"Version":"v1.3.0","Time":"2026-10-21T12:00:00Z","Origin":{"Hash":"preserved"}}`,
		),
		"/@v/" + version + ".info": []byte(`{"Version":"v1.2.3","Time":"2026-10-20T12:00:00Z"}`),
		"/@v/" + version + ".mod":  []byte("module " + goModule + "\n\ngo 1.23\n"),
		"/@v/" + version + ".zip":  goArchive(t, goModule, version),
	}
	for suffix, body := range fixtures {
		w.up.Set("/"+goEscapedModule+suffix, fakeupstream.Resource{Body: body})
		readGo(t, w, src, q, goEscapedModule+suffix, body)
		readGo(t, w, src, q, goModule+suffix, body)
	}
	for _, request := range w.up.Requests() {
		if !strings.HasPrefix(request.Path, "/"+goEscapedModule+"/") {
			t.Fatal("upstream identity was not case escaped", request.Path)
		}
	}
	before := w.up.Count("", "")
	q.Repo.Offline = ptr(true)
	w.clock.Advance(24 * time.Hour)
	for suffix, body := range fixtures {
		w.up.Delete("/" + goEscapedModule + suffix)
		readGo(t, w, src, q, goModule+suffix, body)
	}
	if w.up.Count("", "") != before {
		t.Fatal("offline read contacted upstream")
	}
	q.Resource = source.Resource{Path: goModule + "/@v/v9.0.0+incompatible.zip"}
	if _, err := src.Read(t.Context(), q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("offline miss", err)
	}
}

func TestGoSourceMetadataRefreshAndImmutableFiles(t *testing.T) {
	w, src, q := goSourceFixture(t)
	list := goEscapedModule + "/@v/list"
	w.up.Set("/"+list, fakeupstream.Resource{Body: []byte("v1.0.0\n")})
	readGo(t, w, src, q, list, []byte("v1.0.0\n"))
	w.up.Set("/"+list, fakeupstream.Resource{Body: []byte("v1.0.0\nv1.1.0\n")})
	readGo(t, w, src, q, list, []byte("v1.0.0\n"))
	w.clock.Advance(31 * time.Second)
	readGo(t, w, src, q, list, []byte("v1.0.0\nv1.1.0\n"))
	path := goEscapedModule + "/@v/v1.0.0.mod"
	original := []byte("module " + goModule + "\n")
	w.up.Set("/"+path, fakeupstream.Resource{Body: original})
	readGo(t, w, src, q, path, original)
	before := w.up.Count("GET", "/"+path)
	w.clock.Advance(24 * time.Hour)
	w.up.Set(
		"/"+path,
		fakeupstream.Resource{Body: []byte("module " + goModule + "\n// replaced\n")},
	)
	readGo(t, w, src, q, path, original)
	if w.up.Count("GET", "/"+path) != before {
		t.Fatal("immutable module file refreshed")
	}
}

func TestGoSourcePseudoVersionNegativeCacheAndCancellation(t *testing.T) {
	w, src, q := goSourceFixture(t)
	version := "v0.0.0-20261020120000-abcdefabcdef"
	path := goEscapedModule + "/@v/" + version + ".info"
	q.Resource = source.Resource{Path: path}
	if _, err := src.Read(t.Context(), q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	body := []byte(`{"Version":"` + version + `","Time":"2026-10-20T12:00:00Z"}`)
	w.up.Set("/"+path, fakeupstream.Resource{Body: body})
	if _, err := src.Read(t.Context(), q); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("negative cache bypass", err)
	}
	w.clock.Advance(6 * time.Second)
	readGo(t, w, src, q, path, body)
	before := w.up.Count("", "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := src.Read(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if w.up.Count("", "") != before {
		t.Fatal("canceled caller contacted upstream")
	}
}

func TestGoSourceRejectsInvalidBodiesBeforeAdmission(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		for _, kind := range []string{"list", "info", "mod", "zip", "latest"} {
			t.Run(fmt.Sprintf("%s/noStore%t", kind, noStore), func(t *testing.T) {
				w, src, q := goSourceFixture(t)
				q.Repo.StoreArtifactsLocally = ptr(!noStore)
				path, good := goEscapedModule+"/@v/v1.0.0."+kind, []byte{}
				switch kind {
				case "list":
					path, good = goEscapedModule+"/@v/list", []byte("v1.0.0\n")
				case "latest":
					path, good = goEscapedModule+"/@latest", []byte(`{"Version":"v1.0.0"}`)
				case "info":
					good = []byte(`{"Version":"v1.0.0"}`)
				case "mod":
					good = []byte("module " + goModule + "\n")
				case "zip":
					good = goArchive(t, goModule, "v1.0.0")
				}
				w.up.Set("/"+path, fakeupstream.Resource{Body: []byte("invalid upstream bytes")})
				q.Resource = source.Resource{Path: path}
				if _, err := src.Read(t.Context(), q); err == nil {
					t.Fatal("invalid body accepted")
				}
				resource, _ := GoResource(path, "GET")
				if _, err := w.db.Items().
					Get(t.Context(), w.c.Key, true, resource.Path); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatal("invalid body published", err)
				}
				w.up.Set("/"+path, fakeupstream.Resource{Body: good})
				readGo(t, w, src, q, path, good)
			})
		}
	}
}

func TestGoResourceCanonicalIdentityAndInputChecks(t *testing.T) {
	for _, path := range []string{"sumdb/sum.golang.org/supported", "https://elsewhere.test/@v/list", "../module/@v/list", "example.com/mod/@v/main.info", "example.com/mod/@v/v1.0.0.exe"} {
		if _, err := GoResource(path, "GET"); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatal(path, err)
		}
	}
	if _, err := GoResource(goModule+"/@v/list", "PUT"); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal("write accepted", err)
	}
	alias, err := GoResource(goEscapedModule+"/@v/latest.info", "HEAD")
	if err != nil || alias.Path != goModule+"/@latest" || alias.Method != "HEAD" {
		t.Fatal(alias, err)
	}
	if _, err = (GoSource{}).Read(
		t.Context(),
		source.Request{},
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatal(err)
	}
}

func TestGoBodyValidationRejectsWrongIdentity(t *testing.T) {
	cases := []struct{ path, body string }{
		{"/@v/list", "v1.0.0 bogus-time\n"},
		{"/@v/list", "v1.0.0 2026-10-21T12:00:00Z extra\n"},
		{"/@latest", `{"Version":"main"}`},
		{"/@latest", `{"Version":"v1.0.0"} trailing`},
		{"/@v/v1.0.0.info", `{"Version":"v1.0.1"}`},
		{"/@v/v1.0.0.info", `{"Version":"v1.0.0","Time":"bad"}`},
		{"/@v/v1.0.0.mod", "module example.com/another\n"},
		{"/@v/v1.0.0.mod", "go 1.23\n"},
	}
	for _, tc := range cases {
		r, err := GoResource(goModule+tc.path, "GET")
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Validate(
			t.Context(),
			strings.NewReader(tc.body),
			int64(len(tc.body)),
		); !errors.Is(
			err,
			ErrGoResponse,
		) {
			t.Fatal(tc.path, err)
		}
	}
	r, _ := GoResource(goModule+"/@v/v1.0.0.zip", "GET")
	wrong := goArchive(t, "example.com/another", "v1.0.0")
	if err := r.Validate(
		t.Context(),
		bytes.NewReader(wrong),
		int64(len(wrong)),
	); !errors.Is(
		err,
		ErrGoResponse,
	) {
		t.Fatal(err)
	}
	info, _ := GoResource(goModule+"/@v/v1.0.0.info", "GET")
	if err := info.Validate(
		t.Context(),
		strings.NewReader(""),
		goformat.MaxInfo+1,
	); !errors.Is(
		err,
		ErrGoResponse,
	) {
		t.Fatal(err)
	}
}

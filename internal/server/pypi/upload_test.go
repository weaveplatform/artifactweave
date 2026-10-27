package pypi_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // wire checksum
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/formats"
	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func uploadItemError(_ context.Context, _ string, err error) error {
	var rejection *format.RejectionError
	var denied *item.OverwriteDeniedError
	switch {
	case errors.As(err, &rejection):
		return apierror.New(rejection.Status, rejection.Message)
	case errors.As(err, &denied):
		return apierror.New(http.StatusForbidden, denied.Error())
	case errors.Is(err, item.ErrBadChecksum):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, item.ErrChecksumMismatch):
		return apierror.New(http.StatusConflict, err.Error())
	}
	return err
}

// twineForm deliberately sends fields after content as multipart readers may
// encounter either order. Extra metadata and signatures never choose the path.
func twineForm(
	t *testing.T,
	filename string,
	content []byte,
	fields map[string]string,
) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if filename != "" {
		part, err := writer.CreateFormFile("content", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func (f *fixture) upload(
	t *testing.T,
	path, user, filename string,
	content []byte,
	fields map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, typ := twineForm(t, filename, content, fields)
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", typ)
	req.Header.Set("X-Test-User", user)
	out := httptest.NewRecorder()
	f.h.ServeHTTP(out, req)
	return out
}

func uploadFields(content []byte) map[string]string {
	return map[string]string{
		":action":          "file_upload",
		"protocol_version": "1",
		"sha256_digest":    sha256Hex(content),
		"metadata_version": "2.1",
		"filetype":         "bdist_wheel",
		"pyversion":        "py3",
	}
}

func TestAccUploads(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	for _, kind := range []string{"wheel", "sdist"} {
		name := "Demo_Pkg"
		version := "1.0"
		content := wheel(t, "demo_pkg", version, metadata(name, version, ""))
		filename := "demo_pkg-1.0-py3-none-any.whl"
		if kind == "sdist" {
			content = sdist(t, "demo_pkg", version, metadata(name, version, ""))
			filename = "demo_pkg-1.0.tar.gz"
		}
		fields := uploadFields(content)
		fields["name"] = "demo-pkg"
		fields["version"] = "1.0.0"
		fields["gpg_signature"] = "ignored"
		fields["attestations"] = "ignored"
		fields["comment"] = "ignored"
		sum := md5.Sum(content)
		fields["md5_digest"] = base64.RawURLEncoding.EncodeToString(sum[:])
		b2 := blake2b.Sum256(content)
		fields["blake2_256_digest"] = hex.EncodeToString(b2[:])
		for _, path := range []string{api, strings.TrimSuffix(api, "/")} {
			rec := f.upload(t, path, "deployer", filename, content, fields)
			wantStatus(t, rec, http.StatusOK)
			if rec.Body.Len() != 0 {
				t.Fatalf("upload body: %s", rec.Body)
			}
		}
		path := "demo-pkg/1.0/" + filename
		it, err := f.db.Items().Get(t.Context(), "pypi-local", false, path)
		if err != nil {
			t.Fatal(err)
		}
		props, err := f.db.Items().Properties(t.Context(), it.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := props[pypifmt.PropName]; len(got) != 1 || got[0] != name {
			t.Fatalf("metadata properties: %v", props)
		}
		if _, err := f.db.Items().
			Get(t.Context(), "pypi-local", false, path+".asc"); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Fatalf("signature stored: %v", err)
		}
		// Other bytes at the same canonical path require Delete, checked by the
		// item service against the existing item within the write transaction.
		changed := wheel(t, "demo_pkg", version, metadata(name, version, "Summary: changed"))
		if kind == "sdist" {
			changed = sdist(t, "demo_pkg", version, metadata(name, version, "Summary: changed"))
		}
		wantStatus(
			t,
			f.upload(t, api, "deployer", filename, changed, uploadFields(changed)),
			http.StatusForbidden,
		)
		wantStatus(
			t,
			f.upload(t, api, "writer", filename, changed, uploadFields(changed)),
			http.StatusOK,
		)
	}
}

func TestAccUploadRefusals(t *testing.T) {
	content := wheel(t, "demo", "1.0", metadata("demo", "1.0", ""))
	const filename = "demo-1.0-py3-none-any.whl"
	for _, test := range []struct {
		name, user, path, file string
		body                   []byte
		fields                 map[string]string
		status                 int
	}{
		{"no auth", "", api, filename, content, uploadFields(content), 401},
		{"anonymous", "anonymous", api, filename, content, uploadFields(content), 401},
		{"reader", "reader", api, filename, content, uploadFields(content), 403},
		{"folder", "writer", api + "folder", filename, content, uploadFields(content), 405},
		{"simple folder", "writer", api + "simple/", filename, content, uploadFields(content), 405},
		{"remote", "admin", strings.Replace(api, "pypi-local", "pypi-remote", 1), filename, content, uploadFields(content), 501},
		{"wrong action", "writer", api, filename, content, map[string]string{":action": "submit"}, 400},
		{"no file", "writer", api, "", nil, uploadFields(content), 400},
		{"bad archive", "writer", api, filename, []byte("broken"), uploadFields([]byte("broken")), 400},
		{"wrong extension", "writer", api, "demo.txt", content, uploadFields(content), 400},
		{"form name", "writer", api, filename, content, map[string]string{":action": "file_upload", "name": "other"}, 400},
		{"form version", "writer", api, filename, content, map[string]string{":action": "file_upload", "version": "2.0"}, 400},
		{"sha mismatch", "writer", api, filename, content, map[string]string{":action": "file_upload", "sha256_digest": strings.Repeat("0", 64)}, 400},
		{"bad sha", "writer", api, filename, content, map[string]string{":action": "file_upload", "sha256_digest": "bad"}, 400},
		{"md5 mismatch", "writer", api, filename, content, map[string]string{":action": "file_upload", "md5_digest": strings.Repeat("0", 32)}, 400},
		{"bad md5", "writer", api, filename, content, map[string]string{":action": "file_upload", "md5_digest": "!"}, 400},
		{"blake mismatch", "writer", api, filename, content, map[string]string{":action": "file_upload", "blake2_256_digest": strings.Repeat("0", 64)}, 400},
		{"bad blake", "writer", api, filename, content, map[string]string{":action": "file_upload", "blake2_256_digest": "bad"}, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, pypifmt.Settings{})
			rec := f.upload(t, test.path, test.user, test.file, test.body, test.fields)
			wantStatus(t, rec, test.status)
			if !strings.Contains(rec.Body.String(), `"errors"`) {
				t.Fatalf("wrong envelope: %s", rec.Body)
			}
			if _, err := f.db.Packages().
				Find(t.Context(), "pypi-local", "demo"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("refusal indexed package: %v", err)
			}
		})
	}
	f := newFixture(t, pypifmt.Settings{})
	wantStatus(t, f.do(http.MethodPost, api, "writer", "Content-Type", "text/xml"), 415)
	// Without checksums, equal bytes still require only Deploy.
	for range 2 {
		wantStatus(
			t,
			f.upload(
				t,
				api,
				"deployer",
				filename,
				content,
				map[string]string{":action": "file_upload"},
			),
			200,
		)
	}
}

type inspectedFormat struct {
	pypifmt.Handler
	count *int
}

func (h inspectedFormat) Inspect(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (any, error) {
	*h.count++
	return h.Handler.Inspect(ctx, env, c, w)
}

func TestAccUploadInspectsOnceAndChecksCanonicalPermission(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			f := newFixture(t, pypifmt.Settings{})
			count := 0
			f.items.Formats = func(kind repo.PackageType) format.Handler {
				if kind == repo.PyPI {
					return inspectedFormat{count: &count}
				}
				return formats.For(kind)
			}
			prefix := "demo/**"
			if !allowed {
				stats, err := f.db.WorkQueue().Stats(t.Context())
				if err != nil || len(stats) != 0 {
					t.Fatalf("refused upload queued work: %v, %v", stats, err)
				}
				prefix = "elsewhere/**"
			}
			if err := f.db.Permissions().
				Create(t.Context(), types.Permission{Name: "scoped", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"scoped": types.ActionWrite}}, Targets: map[string]types.Target{"pypi-local": {Include: []string{prefix}}}}}}); err != nil {
				t.Fatal(err)
			}
			content := wheel(t, "demo", "1.0", metadata("demo", "1.0", ""))
			rec := f.upload(
				t,
				api,
				"scoped",
				"demo-1.0-py3-none-any.whl",
				content,
				uploadFields(content),
			)
			want := 403
			if allowed {
				want = 200
			}
			wantStatus(t, rec, want)
			if count != 1 {
				t.Fatalf("Inspect called %d times", count)
			}
			if !allowed {
				if _, err := f.db.Items().
					Get(t.Context(), "pypi-local", false, "demo/1.0/demo-1.0-py3-none-any.whl"); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatalf("refused destination created: %v", err)
				}
				if _, err := f.db.Packages().
					Find(t.Context(), "pypi-local", "demo"); !errors.Is(
					err,
					store.ErrNotFound,
				) {
					t.Fatalf("refused destination indexed: %v", err)
				}
			}
		})
	}
}

func TestAccUploadReservedAndEnforcement(t *testing.T) {
	for _, name := range []string{"packages", "simple"} {
		f := newFixture(t, pypifmt.Settings{})
		content := wheel(t, name, "1.0", metadata(name, "1.0", ""))
		wantStatus(
			t,
			f.upload(
				t,
				api,
				"writer",
				name+"-1.0-py3-none-any.whl",
				content,
				uploadFields(content),
			),
			400,
		)
	}
	for _, settings := range []pypifmt.Settings{{EnforceLayout: true}, {EnforceNamingNormalization: true}} {
		f := newFixture(t, settings)
		content := wheel(t, "demo_pkg", "1.0", metadata("demo_pkg", "1.0", ""))
		wantStatus(
			t,
			f.upload(
				t,
				api,
				"writer",
				"demo-pkg-1.0-py3-none-any.whl",
				content,
				uploadFields(content),
			),
			403,
		)
	}
}

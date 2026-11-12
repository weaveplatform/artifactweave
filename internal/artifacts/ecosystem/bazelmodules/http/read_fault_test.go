package bazelmoduleshttp_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var errDropped = errors.New("the store dropped the connection")

// droppingBlobs opens stored content whose first sound reads, in order or
// at offsets, are served and every later one fails, as a store whose
// connection drops does. Uploads are written as usual.
type droppingBlobs struct {
	blob.Store
	sound int
}

func (b droppingBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return nil, info, err
	}
	return &dropping{ReadSeekCloser: r, sound: b.sound}, info, nil
}

type dropping struct {
	io.ReadSeekCloser
	sound int
}

func (d *dropping) cut() error {
	if d.sound == 0 {
		return errDropped
	}
	d.sound--
	return nil
}

func (d *dropping) Read(p []byte) (int, error) {
	if err := d.cut(); err != nil {
		return 0, err
	}
	return d.ReadSeekCloser.Read(p) //nolint:wrapcheck // the store's own error
}

func (d *dropping) ReadAt(p []byte, off int64) (int, error) {
	if err := d.cut(); err != nil {
		return 0, err
	}
	return d.ReadSeekCloser.(io.ReaderAt).ReadAt(p, off) //nolint:wrapcheck // the store's own error
}

// deployAnswer deploys body at path in the fixture's local repository and
// answers the deploy's error as the item routes' deploy does
// (itemkit.Kit.ItemError written by httpkit.WriteErr).
func (f *fixture) deployAnswer(path string, body []byte) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := httptest.NewRecorder()
	_, err := f.items.Deploy(f.t.Context(), item.Deploy{
		Target: item.Target{Repo: f.local, Path: path}, Body: bytes.NewReader(body), By: "admin",
	})
	if err == nil {
		rec.WriteHeader(http.StatusCreated)
		return rec
	}
	k := itemkit.New(itemkit.Deps{Logger: slog.New(slog.DiscardHandler)})
	r := httptest.NewRequestWithContext(f.t.Context(), http.MethodPut,
		"/artifactory/"+f.local.Key+"/"+path, nil)
	httpkit.WriteErr(rec, r, k.ItemError(r.Context(), "deploy", err))
	return rec
}

func wantDeployError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Fatalf("%d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// A MODULE.bazel or source archive whose stored content fails a read, in
// the MODULE.bazel, the archive's sniff or its walk, answers 500 "internal
// error" and stores nothing, where it answered 409 "invalid Bazel Modules
// manifest or archive" as an invalid publication. An invalid upload is
// still refused 409.
func TestAccDeployAnswersAFailedReadAsAServerFault(t *testing.T) {
	var zipped bytes.Buffer
	z := zip.NewWriter(&zipped)
	if w, err := z.Create("hello-1.0.0/BUILD.bazel"); err != nil {
		t.Fatal(err)
	} else if _, err := io.WriteString(w, "filegroup(name = \"all\")\n"); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	const module = "modules/hello/1.0.0/MODULE.bazel"
	for name, tc := range map[string]struct {
		path  string
		body  []byte
		sound int
	}{
		"MODULE.bazel":        {module, []byte("module(name = \"hello\", version = \"1.0.0\")\n"), 0},
		"the archive's sniff": {"modules/hello/1.0.0/hello-1.0.0.tar.gz", archive(t), 0},
		"the archive's walk":  {"modules/hello/1.0.0/hello-1.0.0.zip", zipped.Bytes(), 1},
	} {
		f := newFixture(t)
		f.items.Blobs = droppingBlobs{Store: f.items.Blobs, sound: tc.sound}
		wantDeployError(t, f.deployAnswer(tc.path, tc.body),
			http.StatusInternalServerError, "internal error")
		_, err := f.db.Items().Get(t.Context(), f.local.Key, false, tc.path)
		if !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s: stored after a failed read: %v", name, err)
		}
	}
	f := newFixture(t)
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{module, []byte("module(name = \"other\", version = \"1.0.0\")\n")},
		{"modules/hello/1.0.0/hello.zip", []byte("not a zip archive")},
		{"modules/hello/1.0.0/hello.tar.gz", []byte("not gzip")},
	} {
		wantDeployError(t, f.deployAnswer(tc.path, tc.body),
			http.StatusConflict, "invalid Bazel Modules manifest or archive")
	}
	rec := f.deployAnswer(module, []byte("module(name = \"hello\", version = \"1.0.0\")\n"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid MODULE.bazel: %d %s", rec.Code, rec.Body)
	}
}

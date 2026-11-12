package luarockshttp_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/platform/archive"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

var errDropped = errors.New("the store dropped the connection")

// readAtFaultBlobs opens content that reads in order as stored, but whose
// reads at offsets fail after the first sound ones, as a store whose
// connection drops does. A rock upload copies the uploaded part in order,
// so only the rock's validation, which reads it as a ZIP, meets the fault.
type readAtFaultBlobs struct {
	blob.Store
	sound int
}

func (b readAtFaultBlobs) Open(
	ctx context.Context, sha string,
) (io.ReadSeekCloser, blob.Info, error) {
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return nil, info, err
	}
	return &readAtFault{ReadSeekCloser: r, sound: b.sound}, info, nil
}

type readAtFault struct {
	io.ReadSeekCloser
	sound int
}

func (c *readAtFault) ReadAt(p []byte, off int64) (int, error) {
	if c.sound == 0 {
		return 0, errDropped
	}
	c.sound--
	return archive.ReaderAt(c.ReadSeekCloser).ReadAt(p, off)
}

// uploadRockTo posts body as the rock filename to the version a rockspec
// upload gave.
func uploadRockTo(
	f *fixture,
	versionID int64,
	filename string,
	body []byte,
) *httptest.ResponseRecorder {
	f.t.Helper()
	form, contentType := form(f.t, "rock_file", filename, body)
	return f.do(http.MethodPost,
		fmt.Sprintf("/artifactory/api/luarocks/rocks/api/1/secret/upload_rock/%d", versionID),
		form, contentType, "admin")
}

// A rock whose stored content fails a read while LuaRocks validates it is
// no bad rock: wherever the read fails, the sniff, the ZIP's directory or
// an entry, the upload answers 500 "internal error", logs the fault and
// stores no rock. A rock that is no ZIP, or whose entry is corrupt, is
// still refused 400 with the same message.
func TestAccRockUploadAnswersAFailedReadAsAServerFault(t *testing.T) {
	body := rock(t, "hello", "1.0-1")
	failed := 0
	for sound := 0; ; sound++ {
		f := newWatched(t, repo.Local)
		version := uploadSpecTo(t, f.fixture, "rocks", "hello", "1.0-1", spec("hello", "1.0-1"))
		f.items.Blobs = readAtFaultBlobs{Store: f.items.Blobs, sound: sound}
		rec := uploadRockTo(f.fixture, version, "hello-1.0-1.src.rock", body)
		if rec.Code == http.StatusOK {
			break
		}
		wantError(t, rec, http.StatusInternalServerError, "internal error")
		if len(f.faults) != 1 || !errors.Is(f.faults[0], errDropped) {
			t.Fatalf("%d sound reads: faults %v", sound, f.faults)
		}
		_, err := f.db.Items().Get(t.Context(), "rocks", false, "hello-1.0-1.src.rock")
		if err == nil {
			t.Fatalf("%d sound reads: the rock was stored", sound)
		}
		failed++
	}
	if failed < 3 {
		t.Fatalf("only %d reads failed: the sniff, the directory and the entry each must", failed)
	}
	unknown := rockWithUnknownMethod(t)
	const invalid = "Unsafe or invalid LuaRocks archive: "
	for name, tc := range map[string]struct {
		body    []byte
		message string
	}{
		"no ZIP":         {[]byte("not a ZIP"), "LuaRocks rock must be a ZIP archive"},
		"corrupt entry":  {rockWithCorruptEntry(t), invalid + "luarocks: read archive entry: zip: checksum error"},
		"unknown method": {unknown, invalid + "luarocks: open archive entry: zip: unsupported compression algorithm"},
	} {
		f := newWatched(t, repo.Local)
		version := uploadSpecTo(t, f.fixture, "rocks", "hello", "1.0-1", spec("hello", "1.0-1"))
		wantError(t, uploadRockTo(f.fixture, version, "hello-1.0-1.src.rock", tc.body),
			http.StatusBadRequest, tc.message)
		if len(f.faults) != 0 {
			t.Fatalf("%s: faults logged: %v", name, f.faults)
		}
	}
}

// A rock deployed through the item routes whose stored content fails a
// read, at the sniff or in the ZIP's walk, answers 500 "internal error", as
// the item routes' deploy maps the item service's error
// (itemkit.Kit.ItemError written by httpkit.WriteErr), and stores no rock.
// It answered 400 "LuaRocks rock must be a ZIP archive" or "Unsafe or
// invalid LuaRocks archive: ...". A rock that is no ZIP is still refused
// 400.
func TestAccRockDeployAnswersAFailedReadAsAServerFault(t *testing.T) {
	const name = "hello-1.0-1.src.rock"
	for what, tc := range map[string]struct {
		body    []byte
		sound   int
		status  int
		message string
	}{
		"failed sniff": {rock(t, "hello", "1.0-1"), 0, http.StatusInternalServerError, "internal error"},
		"failed walk":  {rock(t, "hello", "1.0-1"), 1, http.StatusInternalServerError, "internal error"},
		// sound counts down from -1, never to 0.
		"no ZIP": {[]byte("not a ZIP"), -1, http.StatusBadRequest, "LuaRocks rock must be a ZIP archive"},
	} {
		f := newFixture(t, repo.Local)
		f.items.Blobs = readAtFaultBlobs{Store: f.items.Blobs, sound: tc.sound}
		_, err := f.items.Deploy(t.Context(), item.Deploy{
			Target: item.Target{Repo: f.c, Path: name}, Body: bytes.NewReader(tc.body), By: "admin",
		})
		k := itemkit.New(itemkit.Deps{Logger: slog.New(slog.DiscardHandler)})
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut,
			"/artifactory/rocks/"+name, nil)
		rec := httptest.NewRecorder()
		httpkit.WriteErr(rec, r, k.ItemError(r.Context(), "deploy", err))
		wantError(t, rec, tc.status, tc.message)
		if _, err := f.db.Items().Get(t.Context(), "rocks", false, name); err == nil {
			t.Fatalf("%s: the rock was stored", what)
		}
	}
}

// rockWithUnknownMethod is a rock whose matching rockspec is followed by an
// entry compressed with a method no reader knows, so it lists but cannot be
// opened.
func rockWithUnknownMethod(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, err := z.Create("hello-1.0-1.rockspec")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(spec("hello", "1.0-1")); err != nil {
		t.Fatal(err)
	}
	w, err = z.CreateRaw(&zip.FileHeader{
		Name: "hello.lua", Method: 99, CompressedSize64: 1, UncompressedSize64: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

package pkgroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/testfault"
	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// formFormat answers POST /upload/{file} with what ReadForm read for that
// file part name ("-" for any), and /stored with the file part's content.
func formFormat() pkgroute.Protocol {
	return pkgroute.Protocol{
		Name:         "testfmt",
		Types:        testFormat(nil).Types,
		ErrorProfile: apierror.ProfileNPM,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			r.Post("/upload/{file}", func(w http.ResponseWriter, r *http.Request) {
				file := chi.URLParam(r, "file")
				if file == "-" {
					file = ""
				}
				f, err := k.ReadForm(r, file)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				rc, _, err := k.Blobs().Open(r.Context(), f.File.Digests.SHA256)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				defer func() { _ = rc.Close() }()
				content, err := io.ReadAll(rc)
				if err != nil {
					k.Error(w, r, err)
					return
				}
				_ = json.NewEncoder(w).Encode(formAnswer{
					Content: string(content), Size: f.File.Size, FileName: f.FileName,
					Field: f.Field, Fields: f.Fields,
				})
			})
		},
	}
}

type formAnswer struct {
	Content  string     `json:"content"`
	Size     int64      `json:"size"`
	FileName string     `json:"fileName"`
	Field    string     `json:"field"`
	Fields   url.Values `json:"fields"`
}

// part is one part of a test form: a field, or a file when fileName is set.
type part struct{ name, fileName, value string }

// form encodes parts as multipart/form-data, returning the body and its
// Content-Type.
func form(t *testing.T, parts ...part) (string, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.fileName != "" {
			w, err = mw.CreateFormFile(p.name, p.fileName)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String(), mw.FormDataContentType()
}

// post sends body to /upload/<file> in the local repository.
func (f *kitFixture) post(
	file string,
	body io.Reader,
	contentType string,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, base+local+"/upload/"+file, body)
	r.Header.Set("X-Test-User", "writer")
	r.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// blobCount is how many blobs the fixture's store holds.
func (f *kitFixture) blobCount(t *testing.T) int {
	t.Helper()
	n := 0
	if err := f.items.Blobs.Walk(context.Background(), func(blob.Info) error {
		n++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func newFormFixture(t *testing.T) *kitFixture {
	t.Helper()
	f := newKitFixture(t, nil)
	f.h = f.router(t, f.o, formFormat())
	return f
}

// twine sends its metadata, then the distribution as "content", then a
// signature as another file; the fields on either side are kept.
func TestAccReadForm(t *testing.T) {
	f := newFormFixture(t)
	body, ct := form(t,
		part{name: ":action", value: "file_upload"},
		part{name: "classifiers", value: "A"},
		part{name: "classifiers", value: "B"},
		part{name: "content", fileName: "pkg-1.0.tar.gz", value: "the distribution"},
		part{name: "gpg_signature", fileName: "pkg-1.0.tar.gz.asc", value: "signature"},
		part{name: "sha256_digest", value: "abc"},
	)
	rec := f.post("content", strings.NewReader(body), ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got formAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "the distribution" || got.Size != int64(len("the distribution")) ||
		got.FileName != "pkg-1.0.tar.gz" || got.Field != "content" {
		t.Fatalf("file part %+v", got)
	}
	want := url.Values{
		":action": {"file_upload"}, "classifiers": {"A", "B"}, "sha256_digest": {"abc"},
	}
	if len(got.Fields) != len(want) || strings.Join(got.Fields["classifiers"], ",") != "A,B" ||
		got.Fields.Get(":action") != "file_upload" || got.Fields.Get("sha256_digest") != "abc" {
		t.Fatalf("fields %v, want %v", got.Fields, want)
	}

	// NuGet's push: the first file part is the package, whatever its name,
	// and later ones are dropped.
	body, ct = form(t,
		part{name: "package", fileName: "package.nupkg", value: "first"},
		part{name: "package", fileName: "package.nupkg", value: "second"},
	)
	rec = f.post("-", strings.NewReader(body), ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("any part: %d %s", rec.Code, rec.Body)
	}
	got = formAnswer{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Content != "first" || got.Field != "package" || len(got.Fields) != 0 {
		t.Fatalf("any part: %+v", got)
	}
}

func TestAccReadFormRefusals(t *testing.T) {
	f := newFormFixture(t)
	named, namedType := form(t, part{name: "other", fileName: "x.whl", value: "x"})
	field, fieldType := form(t, part{name: "content", value: "not a file"})
	fieldsOnly, fieldsOnlyType := form(t, part{name: "a", value: "b"})
	// One field takes all but 99 bytes of the allowance, so the next
	// field's name and overhead overrun it before its value is read.
	nearly, nearlyType := form(t,
		part{name: "description", value: strings.Repeat("d", 10<<20-200)},
		part{name: "x", value: "y"},
	)
	tooLong, tooLongType := form(t, part{name: "description", value: strings.Repeat("d", 10<<20)})
	for _, c := range []struct {
		name, file, body, contentType string
		expected                      int
		says                          string
	}{
		{
			"named part missing", "content", named, namedType, http.StatusBadRequest,
			"no file part named 'content'",
		},
		{
			"a field is no file", "content", field, fieldType, http.StatusBadRequest,
			"no file part named 'content'",
		},
		{"no file at all", "-", fieldsOnly, fieldsOnlyType, http.StatusBadRequest, "no file part"},
		{
			"not multipart", "-", "{}", "application/json", http.StatusUnsupportedMediaType,
			"multipart/form-data",
		},
		{"no content type", "-", "x", "", http.StatusUnsupportedMediaType, "multipart/form-data"},
		{
			"no boundary", "-", "x", "multipart/form-data", http.StatusUnsupportedMediaType,
			"multipart/form-data",
		},
		{
			"malformed", "-", "garbage", "multipart/form-data; boundary=b", http.StatusBadRequest,
			"malformed multipart/form-data body",
		},
		{
			"empty", "-", "", "multipart/form-data; boundary=b", http.StatusBadRequest,
			"malformed multipart/form-data body",
		},
		{
			"fields too large", "-", tooLong, tooLongType, http.StatusRequestEntityTooLarge,
			"fields exceed",
		},
		{
			"too many fields", "-", nearly, nearlyType, http.StatusRequestEntityTooLarge,
			"fields exceed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := f.post(c.file, strings.NewReader(c.body), c.contentType)
			if rec.Code != c.expected || !strings.Contains(npmError(t, rec), c.says) {
				t.Fatalf("%d %s, want %d saying %q", rec.Code, rec.Body, c.expected, c.says)
			}
		})
	}
	if len(f.faults) != 0 {
		t.Fatalf("a client's mistake was logged as a fault: %v", f.faults)
	}
}

// A client that goes away mid-stream, in the file part or around it, is
// answered 400, leaves no content committed and is no fault.
func TestAccReadFormDisconnect(t *testing.T) {
	f := newFormFixture(t)
	body, ct := form(t,
		part{name: "name", value: "pkg"},
		part{name: "content", fileName: "pkg.whl", value: strings.Repeat("w", 64<<10)},
		part{name: "gpg_signature", fileName: "pkg.whl.asc", value: strings.Repeat("s", 64<<10)},
	)
	before := f.blobCount(t)
	for name, at := range map[string]int{
		"in a field":           strings.Index(body, "pkg") + 1,
		"in the file part":     strings.Index(body, "www") + 32<<10,
		"in a dropped part":    strings.Index(body, "sss") + 32<<10,
		"before the last part": strings.LastIndex(body, "--") - 4,
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.post("content", testfault.Disconnect(strings.NewReader(body), int64(at)), ct)
			if rec.Code != http.StatusBadRequest ||
				!strings.Contains(npmError(t, rec), "malformed multipart/form-data body") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	// The file part was complete only when the cut came after it, and that
	// content is an orphan for the collector.
	if got := f.blobCount(t) - before; got > 2 {
		t.Fatalf("%d blobs committed", got)
	}
	if len(f.faults) != 0 {
		t.Fatalf("a disconnect was logged as a fault: %v", f.faults)
	}
	if rec := f.post("content", strings.NewReader(body), ct); rec.Code != http.StatusOK {
		t.Fatalf("whole body: %d %s", rec.Code, rec.Body)
	}
}

// A blob store that fails is the item service's error, mapped as the
// server maps it.
func TestAccReadFormStoreFailure(t *testing.T) {
	f := newFormFixture(t)
	errDisk := errors.New("disk full")
	f.items = &item.Service{
		Store: f.db, Blobs: testfault.Blobs{Store: f.items.Blobs, Append: errDisk},
	}
	o := f.o
	var mapped []error
	o.ItemError = func(_ context.Context, _ string, err error) error {
		mapped = append(mapped, err)
		return apierror.New(http.StatusInternalServerError, "internal error")
	}
	f.h = f.router(t, o, formFormat())
	body, ct := form(t, part{name: "content", fileName: "pkg.whl", value: "wheel"})
	rec := f.post("content", strings.NewReader(body), ct)
	if rec.Code != http.StatusInternalServerError || len(mapped) != 1 ||
		!errors.Is(mapped[0], errDisk) {
		t.Fatalf("%d %s, mapped %v", rec.Code, rec.Body, mapped)
	}
}

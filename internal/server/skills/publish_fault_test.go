package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

type part struct {
	name, filename, body string
}

func form(t *testing.T, parts ...part) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		disposition := fmt.Sprintf(`form-data; name=%q`, p.name)
		if p.filename != "" {
			disposition += fmt.Sprintf(`; filename=%q`, p.filename)
		}
		h.Set("Content-Disposition", disposition)
		writer, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, p.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), mw.FormDataContentType()
}

func result(t *testing.T, body []byte, contentType string) error {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/artifactory/api/skills/s/api/v1/skills",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	_, _, err := readPublish(httptest.NewRecorder(), r)
	return err
}

func status(t *testing.T, err error, want int) {
	t.Helper()
	var api *apierror.Error
	if !errors.As(err, &api) || api.Status != want {
		t.Fatalf("error %v, want HTTP %d", err, want)
	}
}

func TestAccPublishMultipartFaults(t *testing.T) {
	good := part{name: "payload", body: `{"slug":"example","version":"1.2.3"}`}
	file := part{name: "files[]", filename: "SKILL.md", body: "# example"}
	valid, ct := form(t, good, file)
	if err := result(t, valid, ct); err != nil {
		t.Fatalf("valid multipart: %v", err)
	}
	status(t, result(t, valid, "application/json"), http.StatusUnsupportedMediaType)
	status(t, result(t, valid, "multipart/form-data"), http.StatusUnsupportedMediaType)
	status(t, result(t, valid[:len(valid)-9], ct), http.StatusBadRequest)
	for _, tc := range []struct {
		name  string
		parts []part
		want  int
	}{
		{"no payload", []part{file}, http.StatusBadRequest},
		{"duplicate payload", []part{good, good, file}, http.StatusBadRequest},
		{"invalid JSON", []part{{name: "payload", body: "["}, file}, http.StatusBadRequest},
		{"unknown part", []part{good, {name: "other", body: "x"}, file}, http.StatusBadRequest},
		{"duplicate file", []part{good, file, file}, http.StatusBadRequest},
		{
			"unsafe path",
			[]part{good, {name: "files[]", filename: "../escape", body: "x"}},
			http.StatusBadRequest,
		},
		{
			"backslash path",
			[]part{good, {name: "files[]", filename: "src\\evil", body: "x"}},
			http.StatusBadRequest,
		},
		{"missing files", []part{good}, http.StatusBadRequest},
		{
			"oversized payload",
			[]part{{name: "payload", body: strings.Repeat("x", maxPayloadBytes+1)}, file},
			http.StatusRequestEntityTooLarge,
		},
		{"oversized tags", []part{{name: "payload", body: `{"slug":"example","version":"1.2.3","tags":["` +
			strings.Repeat("x", 257) + `"]}`}, file}, http.StatusBadRequest},
		{"too many tags", []part{{name: "payload", body: `{"slug":"example","version":"1.2.3","tags":[` +
			strings.Repeat(`"x",`, 64) + `"z"]}`}, file}, http.StatusBadRequest},
		{"long summary", []part{{name: "payload", body: `{"slug":"example","version":"1.2.3","summary":"` +
			strings.Repeat("x", 4097) + `"}`}, file}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := form(t, tc.parts...)
			status(t, result(t, body, contentType), tc.want)
		})
	}
}

func TestAccPublishRejectsMalformedFileDisposition(t *testing.T) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	p, err := mw.CreateFormField("payload")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(p, `{"slug":"example","version":"1.2.3"}`)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files[]"; filename="unterminated`)
	p, err = mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(p, "x")
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	status(t, result(t, b.Bytes(), mw.FormDataContentType()), http.StatusBadRequest)
	status(t, formError(&http.MaxBytesError{Limit: 1}), http.StatusRequestEntityTooLarge)
}

func TestAccPublishCapsFileCountAndTotalBytes(t *testing.T) {
	p := []part{{name: "payload", body: `{"slug":"example","version":"1.2.3"}`}}
	for i := range 258 {
		p = append(p, part{name: "files[]", filename: fmt.Sprintf("f-%03d", i), body: "x"})
	}
	body, ct := form(t, p...)
	status(t, result(t, body, ct), http.StatusRequestEntityTooLarge)

	body, ct = form(t,
		part{name: "payload", body: `{"slug":"example","version":"1.2.3"}`},
		part{name: "files[]", filename: "data.bin", body: strings.Repeat("x", 32<<20+1)})
	status(t, result(t, body, ct), http.StatusRequestEntityTooLarge)
	_, err := bundle([]file{{name: "data.bin", data: bytes.Repeat([]byte("x"), 32<<20)}})
	status(t, err, http.StatusRequestEntityTooLarge)
}

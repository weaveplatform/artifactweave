package bower

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type manifestReader struct{ *bytes.Reader }

func (manifestReader) Close() error { return nil }

type manifestBlobs struct {
	data []byte
	err  error
}

func (b manifestBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	if b.err != nil {
		return nil, blob.Info{}, b.err
	}
	return manifestReader{bytes.NewReader(b.data)}, blob.Info{Size: int64(len(b.data))}, nil
}

func (b manifestBlobs) Stat(context.Context, string) (blob.Info, error) {
	return blob.Info{Size: int64(len(b.data))}, b.err
}

func TestUnitBowerManifestRejectsAmbiguousAndUnsafeMetadata(t *testing.T) {
	for _, body := range []string{
		``, `[]`, `null`, `{"name":"hello","version":"1.0.0","version":"2.0.0"}`,
		`{"name":"hello","version":"1.0.0","dependencies":{"../escape":"1.0.0"}}`,
		`{"name":"hello","version":"1.0.0","dependencies":{"safe":""}}`,
		`{"name":"hello","version":"1.0.0","dependencies":{"safe":"` + strings.Repeat("x", 513) + `"}}`,
		`{"name":"hello","version":"1.0.0"} {}`,
	} {
		if _, err := DecodeManifest([]byte(body)); err == nil {
			t.Errorf("accepted ambiguous manifest %q", body)
		}
	}
	if _, err := DecodeManifest(bytes.Repeat([]byte{'x'}, MaxManifestBytes+1)); err == nil {
		t.Fatal("accepted an oversized manifest")
	}
	var dependencies strings.Builder
	dependencies.WriteString(`{"name":"hello","version":"1.0.0","dependencies":{`)
	for i := range 1001 {
		if i > 0 {
			dependencies.WriteByte(',')
		}
		dependencies.WriteString(`"dep`)
		dependencies.WriteString(strconv.Itoa(i))
		dependencies.WriteString(`": "1.0.0"`)
	}
	dependencies.WriteString(`}}`)
	if _, err := DecodeManifest([]byte(dependencies.String())); err == nil {
		t.Fatal("accepted excessive dependency metadata")
	}
	if _, err := DecodeGitManifest([]byte(`{"name":"hello"}`)); err != nil {
		t.Fatalf("Git tag should provide omitted version: %v", err)
	}
	if _, err := DecodeManifest([]byte(`{"name":"hello"}`)); err == nil {
		t.Fatal("local manifest omitted its version")
	}
}

func TestUnitBowerAdmissionUsesStoredManifestIdentity(t *testing.T) {
	h := Handler{}
	c := repo.Config{Key: "bower", Class: repo.Local, PackageType: repo.Bower}
	path := ManifestPath("hello", "1.0.0")
	good := []byte(`{"name":"hello","version":"1.0.0"}`)
	w := format.Write{
		Op:        format.OpDeploy,
		Path:      path,
		Checksums: types.Checksums{SHA256: strings.Repeat("a", 64)},
	}
	for _, tc := range []struct {
		name string
		w    format.Write
		body []byte
		err  error
		want int
	}{
		{"valid", w, good, nil, 0},
		{"identity mismatch", w, []byte(`{"name":"other","version":"1.0.0"}`), nil, http.StatusBadRequest},
		{"invalid JSON", w, []byte(`{"name":`), nil, http.StatusBadRequest},
		{"empty blob", w, nil, nil, http.StatusBadRequest},
		{"missing blob", w, nil, errors.New("blob missing"), -1},
		{"missing digest", format.Write{Op: format.OpDeploy, Path: path}, good, nil, http.StatusBadRequest},
		{"bad path", format.Write{Op: format.OpDeploy, Path: "hello/../bower.json"}, good, nil, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := format.Env{Blobs: manifestBlobs{data: tc.body, err: tc.err}}
			_, err := h.Admit(t.Context(), env, c, tc.w)
			if tc.want == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.want != 0 && err == nil {
				t.Fatal("admitted invalid manifest")
			}
			if tc.want > 0 {
				var rejection *format.Rejection
				if !errors.As(err, &rejection) || rejection.Status != tc.want {
					t.Fatalf("admission status: %v", err)
				}
			}
		})
	}
	if _, err := h.Admit(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpChecksum, Path: path}); err != nil {
		t.Fatalf("checksum sidecar: %v", err)
	}
	if _, err := h.Inspect(t.Context(), format.Env{}, c,
		format.Write{Op: format.OpChecksum, Path: path}); err != nil {
		t.Fatalf("checksum inspection: %v", err)
	}
	if _, err := h.Inspect(
		t.Context(),
		format.Env{Blobs: manifestBlobs{data: good}},
		c,
		w,
	); err != nil {
		t.Fatalf("manifest inspection: %v", err)
	}
	blocked := c
	blackout := true
	blocked.BlackedOut = &blackout
	if _, err := h.Admit(
		t.Context(),
		format.Env{Blobs: manifestBlobs{data: good}},
		blocked,
		w,
	); err == nil {
		t.Fatal("blacked-out Bower repository admitted a manifest")
	}
}

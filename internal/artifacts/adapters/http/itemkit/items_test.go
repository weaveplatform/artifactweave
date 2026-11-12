package itemkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactshttp "github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// Matrix values are form-decoded as the JFrog CLI encodes them (QueryEscape)
// and Artifactory decodes them (URLDecoder): "+" is a space and "%2B" a
// plus. Keys keep "+", whose trailing use marks a mandatory property.
func TestAccMatrixParameterDecoding(t *testing.T) {
	for name, c := range map[string]struct {
		escaped   string
		expected  model.Properties
		mandatory []string
	}{
		"plus is a space":       {"libs/a.txt;build.name=my+build", model.Properties{"build.name": {"my build"}}, nil},
		"encoded plus is plus":  {"libs/a.txt;v=a%2Bb", model.Properties{"v": {"a+b"}}, nil},
		"percent-encoded space": {"libs/a.txt;v=a%20b", model.Properties{"v": {"a b"}}, nil},
		"mandatory key":         {"libs/a.txt;k+=v", model.Properties{"k": {"v"}}, []string{"k"}},
		"mandatory, plus value": {"libs/a.txt;k+=x+y", model.Properties{"k": {"x y"}}, []string{"k"}},
		"several values":        {"libs/a.txt;k=a,b", model.Properties{"k": {"a", "b"}}, nil},
		"encoded separator":     {"libs/a.txt;k=a%2Cb", model.Properties{"k": {"a", "b"}}, nil},
		"escaped comma":         {"libs/a.txt;k=a%5C%2Cb%2Cc", model.Properties{"k": {"a,b", "c"}}, nil},
		"as jf sends a build": {
			"libs/a.txt;build.name=my+build;build.number=1;build.timestamp=1760000000000",
			model.Properties{
				"build.name": {"my build"}, "build.number": {"1"}, "build.timestamp": {"1760000000000"},
			},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := itemkit.ParseEscapedItemPath(c.escaped)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Props) != len(c.expected) {
				t.Fatalf("props = %v, want %v", req.Props, c.expected)
			}
			for k, v := range c.expected {
				if !slices.Equal(req.Props[k], v) {
					t.Errorf("%s = %q, want %q", k, req.Props[k], v)
				}
			}
			for _, k := range c.mandatory {
				if !req.Mandatory[k] {
					t.Errorf("%s is not mandatory", k)
				}
			}
			if req.Path != "a.txt" {
				t.Errorf("path = %q", req.Path)
			}
		})
	}
	if _, err := itemkit.ParseEscapedItemPath("libs/a.txt;k=%zz"); err == nil {
		t.Error("a malformed escape was accepted")
	}
}

// unwritable is a blob store whose uploads read what they are given and
// then fail to write it, as a full disk or an object store whose
// connection drops does.
type unwritable struct{ blob.Store }

func (u unwritable) BeginUpload(ctx context.Context, o ...blob.UploadOption) (blob.Upload, error) {
	up, err := u.Store.BeginUpload(ctx, o...)
	return writeFailure{up}, err
}

type writeFailure struct{ blob.Upload }

func (writeFailure) Append(_ context.Context, r io.Reader) (int64, error) {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return 0, err
	}
	return 0, errFault
}

// TestUnitExplodeDeployStoreFaultAnswers500: an archive deploy (PUT with
// X-Explode-Archive: true) whose entry the blob store fails to write is the
// server's fault. The item routes answer 500 "internal error" and store
// nothing, where they answered 400 "archive: unusable archive: a.txt:
// fault" as if the archive were bad. An upload that is no usable archive
// is still refused 400 with its text.
func TestUnitExplodeDeployStoreFaultAnswers500(t *testing.T) {
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	if w, err := zw.Create("a.txt"); err != nil {
		t.Fatal(err)
	} else if _, err := io.WriteString(w, "a"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		unwritable bool
		body       []byte
		status     int
		message    string
	}{
		"the store fails the write": {
			true, zipped.Bytes(), http.StatusInternalServerError, "internal error",
		},
		"no usable archive": {
			false, []byte("PK\x03\x04garbage"),
			http.StatusBadRequest, "archive: unusable archive: zip: not a valid zip file",
		},
	} {
		w := kittest.New(t, inmem.New())
		blobs := w.Blobs
		if tc.unwritable {
			blobs = unwritable{w.Blobs}
		}
		api := artifactshttp.New(artifactshttp.Deps{
			Store: w.Store,
			Deps: itemkit.Deps{
				Blobs:  blobs,
				Access: accessapi.API{Authorizer: &accessapitest.Authorizer{}},
				Logger: slog.New(slog.DiscardHandler),
			},
		})
		router := chi.NewRouter()
		api.RegisterItemRoutes(router)
		r := kittest.Request(t, http.MethodPut, "/artifactory/libs-local/up/bundle.zip")
		r.Body = io.NopCloser(bytes.NewReader(tc.body))
		r.ContentLength = int64(len(tc.body))
		r.Header.Set("X-Explode-Archive", "true")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		var body struct {
			Errors []struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		if rec.Code != tc.status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
			len(body.Errors) != 1 || body.Errors[0].Status != tc.status ||
			body.Errors[0].Message != tc.message {
			t.Errorf("%s: %d %s, want %d %q", name, rec.Code, rec.Body, tc.status, tc.message)
		}
		items, _, err := w.Store.Items().Descendants(t.Context(), kittest.LocalKey, false, "",
			kernel.Page{Limit: kernel.MaxLimit})
		if err != nil || len(items) != 0 {
			t.Errorf("%s: stored %v, %v", name, items, err)
		}
	}
}

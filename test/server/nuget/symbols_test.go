package nuget_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const (
	nugetPDBGUID = "030201000504070608090A0B0C0D0E0F"
	nugetPDBKey  = nugetPDBGUID + "FFFFFFFF"
)

func nugetPortablePDB() []byte                     { return support.NuGetPortablePDB() }
func nugetWindowsPDB() []byte                      { return support.NuGetWindowsPDB() }
func nugetPDBPackage(id string, pdb []byte) []byte { return support.NuGetPDBPackage(id, pdb) }

func TestNuGetSymbolServerLifecycle(t *testing.T) {
	for _, c := range []struct {
		name, key string
		body      []byte
	}{{"portable", nugetPDBKey, nugetPortablePDB()}, {"windows", nugetPDBGUID + "2A", nugetWindowsPDB()}} {
		t.Run(c.name, func(t *testing.T) {
			var q *work.Queue
			h, _, _ := nugetServer(t, func(cfg *server.Config) {
				var err error
				q, err = work.New(cfg.Store, work.Options{})
				if err != nil {
					t.Fatal(err)
				}
				items := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: q}
				if err := items.RegisterWork(q); err != nil {
					t.Fatal(err)
				}
				cfg.Work = q
			})
			push := nugetForm(nugetPDBPackage("Debug", c.body))
			ct, body := contentTypeOf(push)
			headers := basic("admin", "pw")
			headers["Content-Type"] = ct
			r := do(t, h, http.MethodPut, nugetV3+"nuget-local/symbols", headers, body)
			if r.Code != 201 {
				t.Fatalf("push %d %s", r.Code, r.Body)
			}
			url := nugetV3 + "nuget-local/APP.PDB/" + strings.ToLower(c.key) + "/App.Pdb"
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				r = do(t, h, method, url, basic("admin", "pw"), "")
				if r.Code != 200 ||
					r.Header().Get("Content-Length") != "" &&
						r.Header().Get("Content-Length") == "0" {
					t.Fatalf("read %s %d %s", method, r.Code, r.Body)
				}
				if method == http.MethodGet && !bytes.Equal(r.Body.Bytes(), c.body) {
					t.Fatal("symbol bytes differ")
				}
				if method == http.MethodHead && r.Body.Len() != 0 {
					t.Fatal("HEAD body")
				}
			}
			headers = basic("admin", "pw")
			headers["Range"] = "bytes=0-15"
			r = do(t, h, http.MethodGet, url, headers, "")
			if r.Code != 206 || !bytes.Equal(r.Body.Bytes(), c.body[:16]) {
				t.Fatalf("range %d %s", r.Code, r.Body)
			}
			etag := r.Header().Get("ETag")
			headers = basic("admin", "pw")
			headers["If-None-Match"] = etag
			r = do(t, h, http.MethodGet, url, headers, "")
			if r.Code != 304 {
				t.Fatalf("conditional %d", r.Code)
			}
			// Reindex repairs a missing extracted item using the committed source.
			derived := "/artifactory/nuget-local/.symbols/app.pdb/" + c.key + "/app.pdb"
			r = do(t, h, http.MethodDelete, derived, basic("admin", "pw"), "")
			if r.Code != 204 {
				t.Fatalf("delete extracted %d %s", r.Code, r.Body)
			}
			r = do(
				t,
				h,
				http.MethodPost,
				"/artifactory/api/nuget/nuget-local/reindex",
				basic("admin", "pw"),
				"",
			)
			if r.Code != 200 {
				t.Fatalf("reindex %d %s", r.Code, r.Body)
			}
			if err := q.EnsureFresh(t.Context(), format.KindReindex, "nuget-local"); err != nil {
				t.Fatal(err)
			}
			r = do(t, h, http.MethodGet, url, basic("admin", "pw"), "")
			if r.Code != 200 {
				t.Fatalf("reindexed %d %s", r.Code, r.Body)
			}
			r = do(
				t,
				h,
				http.MethodDelete,
				"/artifactory/nuget-local/Debug.1.0.0.snupkg",
				basic("admin", "pw"),
				"",
			)
			if r.Code != 204 {
				t.Fatalf("delete source %d %s", r.Code, r.Body)
			}
			r = do(t, h, http.MethodGet, url, basic("admin", "pw"), "")
			var envelope struct {
				Errors []struct {
					Status int `json:"status"`
				} `json:"errors"`
			}
			if r.Code != 404 || json.Unmarshal(r.Body.Bytes(), &envelope) != nil ||
				len(envelope.Errors) != 1 ||
				envelope.Errors[0].Status != 404 {
				t.Fatalf("missing symbol %d %s", r.Code, r.Body)
			}
		})
	}
}

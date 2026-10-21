package server

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	nugetSymbolRead = "GET " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
	nugetSymbolHead = "HEAD " + nugetV3 + "{repoKey}/{file}.pdb/{key}/{file}.pdb"
	nugetPDBGUID    = "030201000504070608090A0B0C0D0E0F"
	nugetPDBKey     = nugetPDBGUID + "FFFFFFFF"
	nugetSymbolPath = ".symbols/app.pdb/" + nugetPDBKey + "/app.pdb"
)

func nugetPortablePDB() []byte {
	b := make([]byte, 120)
	copy(b, "BSJB")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], 1)
	binary.LittleEndian.PutUint32(b[12:], 12)
	copy(b[16:], "PDB v1.0\x00")
	binary.LittleEndian.PutUint16(b[30:], 2)
	binary.LittleEndian.PutUint32(b[32:], 64)
	binary.LittleEndian.PutUint32(b[36:], 32)
	copy(b[40:], "#Pdb\x00")
	binary.LittleEndian.PutUint32(b[48:], 96)
	binary.LittleEndian.PutUint32(b[52:], 24)
	copy(b[56:], "#~\x00")
	for i := range 20 {
		b[64+i] = byte(i)
	}
	return b
}

func nugetWindowsPDB() []byte {
	b := make([]byte, 6*512)
	copy(b, "Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00")
	for off, v := range map[int]uint32{32: 512, 36: 1, 40: 6, 44: 16, 52: 2, 1024: 3, 1536: 2, 1540: 0, 1544: 28, 1548: 5, 2560: 20000404, 2568: 42} {
		binary.LittleEndian.PutUint32(b[off:], v)
	}
	for i := range 16 {
		b[2572+i] = byte(i)
	}
	return b
}

func nugetPDBPackage(id string, pdb []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ name, body string }{
		{id + ".nuspec", `<package><metadata><id>` + id + `</id><version>1.0.0</version><packageTypes><packageType name="SymbolsPackage" /></packageTypes></metadata></package>`},
		{"lib/net10.0/App.pdb", string(pdb)},
	} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store})
		_, _ = io.WriteString(w, e.body)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func seedNuGetSymbols(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	items := &item.Service{Store: db, Blobs: blobs}
	for _, key := range []string{"nuget-local", "nuget-forced"} {
		deployNuGet(
			t,
			db,
			items,
			key,
			"Debug.1.0.0.snupkg",
			nugetPDBPackage("Debug", nugetPortablePDB()),
		)
	}
	freshNuGetSymbols(t, items)
}

func freshNuGetSymbols(t *testing.T, items *item.Service) {
	t.Helper()
	q, err := work.New(items.Store, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := items.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"nuget-local", "nuget-forced"} {
		if err := q.EnsureFresh(t.Context(), nugetfmt.KindSymbols, key); err != nil {
			t.Fatal(err)
		}
	}
}

func nugetSymbolStatusRows() []statusCase {
	p := nugetV3 + "nuget-local/app.pdb/" + nugetPDBKey + "/app.pdb"
	forced := nugetV3 + "nuget-forced/app.pdb/" + nugetPDBKey + "/app.pdb"
	return []statusCase{
		{nugetSymbolRead, p, "admin", "", 200},
		{nugetSymbolRead, strings.ToLower(p), "nugetreader:pw", "", 200},
		{nugetSymbolHead, p, "admin", "", 200},
		{nugetSymbolRead, p, "user", "", 403},
		{nugetSymbolRead, p, "anonymous", "", 401},
		{nugetSymbolRead, p, "", "", 401},
		{nugetSymbolRead, strings.Replace(p, nugetPDBKey, nugetPDBGUID+"1", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, nugetPDBKey, "bad", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "missing", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "libs-local", 1), "admin", "", 404},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "nuget-remote", 1), "admin", "", 501},
		{nugetSymbolRead, strings.Replace(p, "nuget-local", "nuget-virtual", 1), "admin", "", 501},
		{nugetSymbolRead, forced, "anonymous", "", 401},
		{nugetSymbolRead, forced, "admin", "", 200},
		{nugetSymbolHead, p, "user", "", 403},
		{nugetSymbolHead, forced, "anonymous", "", 401},
	}
}

func TestNuGetSymbolServerLifecycle(t *testing.T) {
	for _, c := range []struct {
		name, key string
		body      []byte
	}{{"portable", nugetPDBKey, nugetPortablePDB()}, {"windows", nugetPDBGUID + "2A", nugetWindowsPDB()}} {
		t.Run(c.name, func(t *testing.T) {
			var q *work.Queue
			h, _, _ := nugetServer(t, func(cfg *Config) {
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

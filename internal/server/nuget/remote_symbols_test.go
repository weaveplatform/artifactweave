package nuget_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const remotePDBKey = "030201000504070608090A0B0C0D0E0FFFFFFFFF"

func remotePDB() []byte {
	b := make([]byte, 120)
	copy(b, "BSJB")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], 1)
	binary.LittleEndian.PutUint32(b[12:], 12)
	copy(b[16:], "PDB v1.0\x00")
	binary.LittleEndian.PutUint16(b[30:], 2)
	for off, v := range map[int]uint32{32: 64, 36: 32, 48: 96, 52: 24} {
		binary.LittleEndian.PutUint32(b[off:], v)
	}
	copy(b[40:], "#Pdb\x00")
	copy(b[56:], "#~\x00")
	for i := range 20 {
		b[64+i] = byte(i)
	}
	return b
}

func TestAccRemoteSymbolBytesRangeCacheAndPermissions(t *testing.T) {
	for _, persist := range []bool{false, true} {
		t.Run(
			strings.ToLower(http.StatusText(200))+string(rune('0'+boolInt(persist))),
			func(t *testing.T) {
				f := newLegacyFixture(t)
				c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
				c.SymbolServerURL = f.up.URL + "/ssqp"
				c.StoreArtifactsLocally = &persist
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				raw := remotePDB()
				path := "app.pdb/" + remotePDBKey + "/app.pdb"
				f.up.Set(
					"/ssqp/"+path,
					fakeupstream.Resource{
						Body:   raw,
						Header: http.Header{"Content-Type": {"application/octet-stream"}},
					},
				)
				url := remoteV3 + "App.PDB/" + strings.ToLower(remotePDBKey) + "/App.PDB"
				rec := f.do("GET", url, "partial")
				wantStatus(t, rec, 403)
				if f.up.Count("GET", "/ssqp/"+path) != 0 {
					t.Fatal("package-only grant fetched symbols")
				}
				rec = f.do("GET", url, "reader")
				wantStatus(t, rec, 200)
				if !bytes.Equal(rec.Body.Bytes(), raw) {
					t.Fatal("symbol bytes changed")
				}
				if !persist {
					rec = f.do("HEAD", url, "reader")
					wantStatus(t, rec, 200)
					if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "120" {
						t.Fatal("HEAD", rec.Header(), rec.Body.Len())
					}
					rec = f.do("GET", url, "reader", "Range", "bytes=10-19")
					wantStatus(t, rec, 206)
					if !bytes.Equal(rec.Body.Bytes(), raw[10:20]) {
						t.Fatal("range", rec.Body.Bytes())
					}
				} else {
					f.up.Close()
					rec = f.do("GET", url, "reader")
					wantStatus(t, rec, 200)
					if !bytes.Equal(rec.Body.Bytes(), raw) {
						t.Fatal("offline symbols")
					}
				}
				wantStatus(t, f.do("GET", url, "nobody"), 403)
			},
		)
	}
}

func TestAccRemoteSymbolBadContentNeverPublishes(t *testing.T) {
	for _, fault := range []string{"key", "html", "truncated", "missing", "redirect"} {
		t.Run(fault, func(t *testing.T) {
			f := newLegacyFixture(t)
			c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
			c.SymbolServerURL = f.up.URL + "/symbols"
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			path := "app.pdb/" + remotePDBKey + "/app.pdb"
			raw := remotePDB()
			resource := fakeupstream.Resource{Body: raw}
			switch fault {
			case "key":
				resource.Body[64] = 99
			case "html":
				resource.Body = []byte("<html>login required</html>")
			case "truncated":
				resource.Fault.TruncateAfter = 50
			case "missing":
				resource.Status = 404
			case "redirect":
				resource.Redirect = "http://169.254.169.254/latest"
			}
			f.up.Set("/symbols/"+path, resource)
			status := 502
			if fault == "missing" {
				status = 404
			}
			wantStatus(t, f.do("GET", remoteV3+path, "reader"), status)
			if _, err := f.db.Items().
				Get(t.Context(), c.Key, true, ".symbols/"+path); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatal("invalid symbol published", err)
			}
		})
	}
}

func TestAccRemoteSymbolFinalAuthorizationFence(t *testing.T) {
	f := newLegacyFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.SymbolServerURL = f.up.URL + "/symbols"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	path := "app.pdb/" + remotePDBKey + "/app.pdb"
	f.up.Set("/symbols/"+path, fakeupstream.Resource{Body: remotePDB()})
	original := f.svc.HTTP
	revoked := false
	f.svc.HTTP = backendFunc(
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			out, err := original.Read(ctx, q, p)
			revoked = true
			return out, err
		},
	)
	f.svc.Revalidate = func(context.Context, source.Request, source.Permit) error {
		if revoked {
			return source.ErrDenied
		}
		return nil
	}
	rec := f.do("GET", remoteV3+path, "reader")
	wantStatus(t, rec, 403)
	if bytes.Contains(rec.Body.Bytes(), []byte("BSJB")) {
		t.Fatal("revoked symbol delivered")
	}
}

func TestAccRemoteSymbolConfigurationAndSizeRefusals(t *testing.T) {
	path := "app.pdb/" + remotePDBKey + "/app.pdb"
	f := newLegacyFixture(t)
	wantStatus(t, f.do("GET", remoteV3+path, "reader"), 404)
	for _, configured := range []string{"https://user:secret@upstream.example/", "file:///etc", "http://169.254.169.254/latest"} {
		c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
		c.SymbolServerURL = configured
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, f.do("GET", remoteV3+path, "reader"), 502)
	}
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.SymbolServerURL = f.up.URL + "/symbols"
	c.StoreArtifactsLocally = new(false)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, 64<<20+1)
	copy(oversized, remotePDB())
	f.up.Set("/symbols/"+path, fakeupstream.Resource{Body: oversized})
	wantStatus(t, f.do("GET", remoteV3+path, "reader"), 502)
}

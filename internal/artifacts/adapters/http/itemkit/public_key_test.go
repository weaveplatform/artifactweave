package itemkit_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

type publicExportBlobs struct {
	blob.Store
	opens, closes        int
	fail                 error
	afterOpen            func()
	seekError, readError error
	oversized            bool
}

func (b *publicExportBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	b.opens++
	if b.fail != nil {
		return nil, blob.Info{}, b.fail
	}
	r, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return nil, info, err
	}
	if b.afterOpen != nil {
		f := b.afterOpen
		b.afterOpen = nil
		f()
	}
	if b.oversized {
		info.Size = 65 << 20
	}
	return &publicExportReader{ReadSeekCloser: r, b: b}, info, nil
}

type publicExportReader struct {
	io.ReadSeekCloser
	b *publicExportBlobs
}

func (r *publicExportReader) Close() error { r.b.closes++; return r.ReadSeekCloser.Close() }
func (r *publicExportReader) Read(p []byte) (int, error) {
	if r.b.readError != nil {
		return 0, r.b.readError
	}
	return r.ReadSeekCloser.Read(p)
}

func (r *publicExportReader) Seek(o int64, w int) (int64, error) {
	if r.b.seekError != nil {
		return 0, r.b.seekError
	}
	return r.ReadSeekCloser.Seek(o, w)
}

func TestUnitRPMStoredPublicKeyGuardsSelectedPayload(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, body, header, value string
		want                              int
	}{
		{"valid GET", "GET", key.Public, "", "", 200},
		{"valid HEAD", "HEAD", key.Public, "", "", 200},
		{"valid range", "GET", key.Public, "Range", "bytes=0-9", 206},
		{"tainted GET", "GET", key.Public + key.Private, "", "", 500},
		{"tainted HEAD", "HEAD", key.Public + key.Private, "", "", 500},
		{"tainted range", "GET", key.Public + key.Private, "Range", "bytes=0-9", 500},
		{"valid conditional", "GET", key.Public, "If-None-Match", "*", 200},
		{"read fault", "GET", key.Public, "", "", 500},
		{"open fault", "GET", key.Public, "", "", 500},
		{"oversized", "GET", key.Public, "", "", 500},
		{"rewind fault", "GET", key.Public, "", "", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			c := *repo.Defaults(&repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM})
			if err := w.Store.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			it := w.Put(t, c, false, "repodata/repomd.xml.key", []byte(tc.body))
			b := &publicExportBlobs{Store: w.Blobs}
			if tc.name == "open fault" {
				b.fail = errors.New("synthetic storage open failure")
			}
			if tc.name == "oversized" {
				b.oversized = true
			}
			if tc.name == "read fault" {
				b.readError = errors.New("synthetic read failure")
			}
			if tc.name == "rewind fault" {
				b.seekError = errors.New("synthetic rewind failure")
			}
			k := w.Kit(itemkit.Deps{Blobs: b, Logger: slog.New(slog.DiscardHandler)})
			r := kittest.Request(t, tc.method, "/artifactory/rpm-local/repodata/repomd.xml.key")
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			if tc.name == "valid conditional" {
				r.Header.Set("If-None-Match", it.Actual.SHA1)
				tc.want = 304
			}
			rec := httptest.NewRecorder()
			k.GetItem(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("public artifact status%d want%d (body withheld)", rec.Code, tc.want)
			}
			wantClose := 1
			if tc.name == "open fault" {
				wantClose = 0
			}
			if b.opens != 1 || b.closes != wantClose {
				t.Fatalf("public artifact ownership: opens%d closes%d", b.opens, b.closes)
			}
			if tc.want == 200 && tc.method == "GET" && rec.Body.String() != key.Public {
				t.Fatal("valid public bytes changed")
			}
			if tc.want == 206 && rec.Body.String() != key.Public[:10] {
				t.Fatal("public range changed")
			}
			if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
				t.Fatal("private fixture material escaped (bytes withheld)")
			}
		})
	}
	// A selected snapshot stays the one checked even if another write replaces
	// the path while the blob is opened; the stream is neither resolved nor opened twice.
	t.Run("replacement after selection", func(t *testing.T) {
		w := kittest.New(t, inmem.New())
		c := *repo.Defaults(&repo.Config{Key: "rpm-local", Class: repo.Local, PackageType: repo.RPM})
		if err := w.Store.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		original := w.Put(t, c, false, "repodata/repomd.xml.key", []byte(key.Public))
		b := &publicExportBlobs{Store: w.Blobs}
		b.afterOpen = func() { w.Put(t, c, false, "repodata/repomd.xml.key", []byte(key.Private)) }
		rec := httptest.NewRecorder()
		w.Kit(itemkit.Deps{Blobs: b}).GetItem(rec, kittest.Request(t, "GET", "/artifactory/rpm-local/repodata/repomd.xml.key"))
		if rec.Code != 200 || rec.Body.String() != key.Public || b.opens != 1 || b.closes != 1 {
			t.Fatal("selected public snapshot was replaced or reopened")
		}
		current, err := w.Store.Items().Get(t.Context(), c.Key, false, "repodata/repomd.xml.key")
		if err != nil || current.Actual.SHA256 == original.Actual.SHA256 {
			t.Fatal("substitution control did not replace the stored item")
		}
	})
}

func TestUnitRPMStoredPublicKeyKeepsReadPolicyAndOtherPaths(t *testing.T) {
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                  string
		kind                  repo.PackageType
		class                 repo.Class
		path                  string
		cached, deny, exclude bool
		want, opens           int
	}{
		{"denied", repo.RPM, repo.Local, "repodata/repomd.xml.key", false, true, false, 403, 0},
		{"excluded", repo.RPM, repo.Local, "repodata/repomd.xml.key", false, false, true, 404, 0},
		{"other file", repo.RPM, repo.Local, "README.txt", false, false, false, 200, 1},
		{"other kind", repo.Generic, repo.Local, "repodata/repomd.xml.key", false, false, false, 200, 1},
		{"remote cache", repo.RPM, repo.Remote, "repodata/repomd.xml.key", true, false, false, 200, 1},
		{"federated", repo.RPM, repo.Federated, "arch/repodata/repomd.xml.key", false, false, false, 500, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := kittest.New(t, inmem.New())
			c := *repo.Defaults(&repo.Config{Key: "export", Class: tc.class, PackageType: tc.kind})
			if tc.exclude {
				pattern := "repodata/**"
				c.ExcludesPattern = &pattern
			}
			if err := w.Store.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			w.Put(t, c, tc.cached, tc.path, []byte(key.Private))
			b := &publicExportBlobs{Store: w.Blobs}
			k := w.Kit(itemkit.Deps{Blobs: b, Logger: slog.New(slog.DiscardHandler)})
			repoKey := c.Key
			if tc.cached {
				repoKey += "-cache"
			}
			r := kittest.Request(t, "GET", "/artifactory/"+repoKey+"/"+tc.path)
			if tc.deny {
				r = r.WithContext(accessapi.WithCaller(t.Context(), kernel.Principal{Name: "reader"}, &accessapitest.Decider{}))
			}
			rec := httptest.NewRecorder()
			k.GetItem(rec, r)
			if rec.Code != tc.want || b.opens != tc.opens {
				t.Fatalf("guard scope: status%d opens%d want%d/%d (body withheld)", rec.Code, b.opens, tc.want, tc.opens)
			}
		})
	}
}

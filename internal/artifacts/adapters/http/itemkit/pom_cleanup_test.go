package itemkit

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type pomBody struct {
	*strings.Reader
	closed bool
	fail   bool
}

func (b *pomBody) Close() error { b.closed = true; return nil }
func (b *pomBody) Read(p []byte) (int, error) {
	if b.fail {
		return 0, io.ErrUnexpectedEOF
	}
	return b.Reader.Read(p)
}

func TestUnitPOMRemoteDeliveryRetainsAuthorityAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, method             string
		refuse, fail, usageError bool
		body                     string
		status                   int
		used                     bool
	}{
		{name: "get", method: "GET", status: 200, used: true},
		{name: "head", method: "HEAD", status: 200},
		{name: "unchanged-get", method: "GET", body: `<project><name>raw &amp; exact</name></project>`, status: 200, used: true},
		{name: "unchanged-head", method: "HEAD", body: `<project><name>raw &amp; exact</name></project>`, status: 200},
		{name: "revoked", method: "GET", refuse: true, status: 403},
		{name: "broken-body", method: "GET", fail: true, status: 404},
		{name: "usage-fault", method: "GET", usageError: true, status: 200, used: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := inmem.New()
			v := repo.Config{Key: "v", Class: repo.Virtual, PackageType: repo.Maven}
			if err := db.Repos().Create(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			k := New(Deps{Store: db, Blobs: blobs})
			data := tc.body
			if data == "" {
				data = `<project><repositories/><name>kept</name></project>`
			}
			body := &pomBody{Reader: strings.NewReader(data), fail: tc.fail}
			h := blob.NewHasher()
			_, _ = h.Write([]byte(data))
			d := h.Sum()
			called, used := 0, false
			result := source.Result{
				Stream: &source.Stream{
					Status:         200,
					Body:           body,
					Representation: source.Representation{Size: body.Size(), Checksums: kernel.Checksums{SHA256: d.SHA256, SHA1: d.SHA1, MD5: d.MD5}},
				},
				Revalidate: func(context.Context) error {
					called++
					if tc.refuse {
						return source.ErrDenied
					}
					return nil
				},
				Used: func(context.Context) error {
					used = true
					if tc.usageError {
						return kernel.ErrInvalid
					}
					return nil
				},
			}
			found := Found{Repo: repo.Config{Key: "member", Class: repo.Remote, PackageType: repo.Maven}, remote: &remoteRead{result: result}}
			r := httptest.NewRequest(tc.method, "/artifactory/v/a.pom", nil)
			rec := httptest.NewRecorder()
			k.serveReturnedPOM(rec, r, db, v, ItemRequest{Key: "v", Path: "a.pom"}, found, kernel.Principal{Name: "reader"})
			if rec.Code != tc.status || used != tc.used || !body.closed {
				t.Fatalf("status %d, used %v, closed %v: %s", rec.Code, used, body.closed, rec.Body.String())
			}
			if tc.body != "" && tc.method == "GET" && rec.Body.String() != tc.body {
				t.Fatalf("unchanged source bytes: %q", rec.Body.String())
			}
			if tc.status == 200 && called != 2 {
				t.Fatalf("authority check not retained: %d", called)
			}
		})
	}
}

type pomCopyFaultStore struct {
	Store
	write bool
}

func (s pomCopyFaultStore) VirtualCopies() virtual.CopyStore {
	return pomCopyFaults{CopyStore: s.Store.VirtualCopies(), write: s.write}
}

type pomCopyFaults struct {
	virtual.CopyStore
	write bool
}

func (s pomCopyFaults) Get(ctx context.Context, key, path string) (virtual.Copy, error) {
	if !s.write {
		return virtual.Copy{}, kernel.ErrInvalid
	}
	return s.CopyStore.Get(ctx, key, path)
}
func (s pomCopyFaults) Put(context.Context, virtual.Copy) error { return kernel.ErrInvalid }

func TestUnitPOMCopyMetadataFaults(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			db := inmem.New()
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			k := New(Deps{Store: db, Blobs: blobs})
			upload, err := blobs.BeginUpload(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = upload.Append(t.Context(), strings.NewReader(`<project><repositories/></project>`)); err != nil {
				t.Fatal(err)
			}
			info, err := upload.Commit(t.Context(), blob.Digests{})
			if err != nil {
				t.Fatal(err)
			}
			f := Found{Repo: repo.Config{Key: "local", Class: repo.Local}}
			f.Item.Actual.SHA256 = info.Digests.SHA256
			_, err = k.pomBeforeReturn(
				httptest.NewRequest("GET", "/", nil),
				pomCopyFaultStore{Store: db, write: write},
				repo.Config{Key: "v", Class: repo.Virtual, PackageType: repo.Maven},
				"a.pom",
				f,
			)
			var answer *apierror.Error
			if !errors.As(err, &answer) || answer.Status != 404 {
				t.Fatal(err)
			}
		})
	}
	k := new(Kit)
	for _, tc := range []struct {
		err    error
		status int
	}{{kernel.ErrUnauthenticated, 401}, {kernel.ErrForbidden, 403}, {&format.RejectionError{Status: 409}, 409}, {apierror.New(404, "member refusal"), 404}} {
		var answer *apierror.Error
		if !errors.As(k.pomError("Failed to transform pom file", tc.err), &answer) || answer.Status != tc.status {
			t.Fatal(tc, answer)
		}
	}
}

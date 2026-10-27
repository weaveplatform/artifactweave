package gitlfs_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func rawLFSRequest(
	t *testing.T,
	f *fixture,
	method, path, body, contentType, accept, user string,
) (int, string) {
	t.Helper()
	req, err := http.NewRequest(
		method,
		f.server.URL+"/artifactory/api/lfs/lfs/"+path,
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if user != "" {
		req.Header.Set("X-User", user)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(data)
}

func TestUnitBatchRejectsMalformedClientRequests(t *testing.T) {
	f := newFixture(t, repo.Local)
	oid := strings.Repeat("a", 64)
	valid := `{"operation":"download","objects":[{"oid":"` + oid + `","size":1}]}`
	for _, tc := range []struct {
		name        string
		body        string
		contentType string
		accept      string
		path        string
		expected    int
	}{
		{name: "wrong content type", body: valid, contentType: "application/json", expected: http.StatusUnsupportedMediaType},
		{name: "unsupported accept", body: valid, contentType: "application/vnd.git-lfs+json", accept: "application/json", expected: http.StatusUnsupportedMediaType},
		{name: "query on batch", body: valid, contentType: "application/vnd.git-lfs+json", path: "?tracking=1", expected: http.StatusUnsupportedMediaType},
		{name: "truncated JSON", body: `{"operation":`, contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "trailing JSON", body: valid + `{}`, contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "unsupported operation", body: strings.Replace(valid, `download`, `delete`, 1), contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "wrong hash algorithm", body: strings.TrimSuffix(valid, `}`) + `,"hash_algo":"sha1"}`, contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "empty objects", body: `{"operation":"download","objects":[]}`, contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "unsupported transfer", body: strings.TrimSuffix(valid, `}`) + `,"transfers":["multipart"]}`, contentType: "application/vnd.git-lfs+json", expected: http.StatusBadRequest},
		{name: "invalid object ID", body: strings.Replace(valid, oid, "bad-oid", 1), contentType: "application/vnd.git-lfs+json", expected: http.StatusUnprocessableEntity},
		{name: "negative object size", body: strings.Replace(valid, `"size":1`, `"size":-1`, 1), contentType: "application/vnd.git-lfs+json", expected: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawLFSRequest(
				t,
				f,
				http.MethodPost,
				"objects/batch"+tc.path,
				tc.body,
				tc.contentType,
				tc.accept,
				"",
			)
			if status != tc.expected || strings.Contains(body, "\"actions\"") {
				t.Fatalf(
					"malformed batch status=%d body=%q, want %d without actions",
					status,
					body,
					tc.expected,
				)
			}
		})
	}
}

func TestUnitObjectRoutesRejectUnsafeRequests(t *testing.T) {
	oid := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		class    repo.Class
		method   string
		path     string
		body     string
		user     string
		expected int
	}{
		{name: "short ID", class: repo.Local, method: http.MethodGet, path: "objects/bad", expected: http.StatusBadRequest},
		{name: "invalid size", class: repo.Local, method: http.MethodGet, path: "objects/" + oid + "?size=wrong", expected: http.StatusBadRequest},
		{name: "missing object", class: repo.Local, method: http.MethodGet, path: "objects/" + oid, expected: http.StatusNotFound},
		{name: "denied read", class: repo.Local, method: http.MethodGet, path: "objects/" + oid, user: "intruder", expected: http.StatusForbidden},
		{name: "virtual requires size", class: repo.Virtual, method: http.MethodGet, path: "objects/" + oid, expected: http.StatusBadRequest},
		{name: "invalid upload ID", class: repo.Local, method: http.MethodPut, path: "objects/bad?size=1", body: "x", expected: http.StatusBadRequest},
		{name: "missing upload size", class: repo.Local, method: http.MethodPut, path: "objects/" + oid, body: "x", expected: http.StatusBadRequest},
		{name: "upload size mismatch", class: repo.Local, method: http.MethodPut, path: "objects/" + oid + "?size=2", body: "x", expected: http.StatusBadRequest},
		{name: "read-only upload", class: repo.Local, method: http.MethodPut, path: "objects/" + oid + "?size=1", body: "x", user: "reader", expected: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.class)
			status, body := rawLFSRequest(t, f, tc.method, tc.path, tc.body, "", "", tc.user)
			if status != tc.expected || bytes.Contains([]byte(body), []byte("\"actions\"")) {
				t.Fatalf(
					"unsafe object request status=%d body=%q, want %d",
					status,
					body,
					tc.expected,
				)
			}
		})
	}
}

func TestUnitVirtualObjectRouteDoesNotDiscloseMissingOrDeniedMembers(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	oid := strings.Repeat("a", 64)
	path := "objects/" + oid + "?size=1"
	for _, tc := range []struct {
		name     string
		user     string
		expected int
	}{
		{name: "permitted but absent", expected: http.StatusNotFound},
		{name: "ungranted reader", user: "intruder", expected: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawLFSRequest(t, f, http.MethodGet, path, "", "", "", tc.user)
			if status != tc.expected || strings.Contains(body, oid) {
				t.Fatalf("virtual missing/denied object status=%d body=%q", status, body)
			}
		})
	}
}

func TestUnitDirectObjectReadHonorsDigestAndDeclaredSize(t *testing.T) {
	f := newFixture(t, repo.Local)
	object := "integrity-bound Git LFS object"
	sum := sha256.Sum256([]byte(object))
	oid := hex.EncodeToString(sum[:])
	path := "objects/" + oid + "?size=" + strconv.Itoa(len(object))
	status, body := rawLFSRequest(t, f, http.MethodPut, path, object, "", "", "")
	if status != http.StatusOK {
		t.Fatalf("seed object status=%d body=%q", status, body)
	}
	status, body = rawLFSRequest(t, f, http.MethodHead, path, "", "", "", "")
	if status != http.StatusOK || body != "" {
		t.Fatalf("HEAD object status=%d body=%q", status, body)
	}
	status, body = rawLFSRequest(
		t,
		f,
		http.MethodGet,
		"objects/"+oid+"?size="+strconv.Itoa(len(object)+1),
		"", "", "", "",
	)
	if status != http.StatusNotFound || strings.Contains(body, object) {
		t.Fatalf("wrong-size object disclosed: status=%d body=%q", status, body)
	}
}

func TestUnitVirtualStaleMembersFailClosed(t *testing.T) {
	oid := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name     string
		setup    func(*testing.T, *fixture, *repo.Config)
		op       string
		expected int
	}{
		{
			name: "deployment target removed", op: "upload", expected: http.StatusMethodNotAllowed,
			setup: func(_ *testing.T, _ *fixture, c *repo.Config) {
				c.DefaultDeploymentRepo = "missing"
			},
		},
		{
			name: "duplicate member", op: "download", expected: http.StatusBadRequest,
			setup: func(_ *testing.T, _ *fixture, c *repo.Config) {
				c.Repositories = []string{"lfs-local", "lfs-local"}
			},
		},
		{
			name: "missing readable member", op: "download", expected: http.StatusNotFound,
			setup: func(_ *testing.T, _ *fixture, c *repo.Config) {
				c.Repositories = []string{"missing", "lfs-local"}
			},
		},
		{
			name: "wrong package member", op: "download", expected: http.StatusBadRequest,
			setup: func(t *testing.T, f *fixture, c *repo.Config) {
				wrong := *repo.Defaults(&repo.Config{
					Key: "wrong", Class: repo.Local, PackageType: repo.NPM,
				})
				if err := f.db.Repos().Create(t.Context(), wrong); err != nil {
					t.Fatal(err)
				}
				c.Repositories = []string{"wrong", "lfs-local"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Virtual)
			c, err := f.db.Repos().Get(t.Context(), "lfs")
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, f, &c)
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			status, reply := f.batch(tc.op, oid, 1)
			if status != http.StatusOK || len(reply.Objects) != 1 ||
				reply.Objects[0].Error == nil || reply.Objects[0].Error.Code != tc.expected ||
				len(reply.Objects[0].Actions) != 0 {
				t.Fatalf("stale virtual member response: status=%d, body=%+v", status, reply)
			}
		})
	}
}

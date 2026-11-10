package vcs_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestAccVCSCustomRootlessFilePrecedesNestedBasename(t *testing.T) {
	data, err := os.ReadFile(repopath.Path(t,
		"internal/artifacts/ecosystem/vcs/http/testdata/legacy-vcs-archive.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	read := sourceBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			body := data
			if q.Resource.Kind == source.ResourceKindVersions {
				body = bytes.ReplaceAll(
					vcsTestRefs(),
					[]byte(vcsTestOID),
					[]byte("81305b86f97f1361e68f51397943eff6dd635520"),
				)
			} else if err := q.Resource.Validate(ctx, bytes.NewReader(data), int64(len(data))); err != nil {
				t.Fatal(err)
			}
			return source.Result{
				Stream: &source.Stream{
					Body:           io.NopCloser(bytes.NewReader(body)),
					Status:         200,
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	w := newItemWorld(
		t,
		false,
		func(c *Config) { c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read} },
	)
	c := repo.Defaults(
		&repo.Config{
			Key:               "custom-rootless",
			Class:             repo.Remote,
			PackageType:       repo.VCS,
			URL:               "https://custom.example",
			VCSGitProvider:    repo.CustomVCS,
			VCSGitDownloadURL: "archives/{2}.{3}",
		},
	)
	if err := w.db.Repos().Create(t.Context(), *c); err != nil {
		t.Fatal(err)
	}
	res := w.do(
		http.MethodGet,
		"/artifactory/api/vcs/downloadTagFile/custom-rootless/team/project/v1.0.0!README.md",
		"reader",
		"",
	)
	if res.Code != 200 {
		t.Fatalf("rootless CUSTOM request now returns %d: %s", res.Code, res.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	r, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "root readme\n" {
		t.Fatalf("rootless CUSTOM README.md got %q; want root readme", got)
	}
}

func TestAccVCSPeerFileUsageRequiresSuccessfulDelivery(t *testing.T) {
	data, err := os.ReadFile(repopath.Path(t,
		"internal/artifacts/ecosystem/vcs/http/testdata/legacy-vcs-readme.zip"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, user string
		deny, failWrite    bool
		status, used       int
	}{
		{name: "GET", method: http.MethodGet, user: "reader", status: http.StatusOK, used: 1},
		{name: "HEAD", method: http.MethodHead, user: "reader", status: http.StatusMethodNotAllowed},
		{name: "denied", method: http.MethodGet, user: "user", status: http.StatusForbidden},
		{name: "revalidation denied", method: http.MethodGet, user: "reader", deny: true, status: http.StatusForbidden},
		{name: "response write fails", method: http.MethodGet, user: "reader", failWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, revalidated := 0, 0
			read := sourceBackend(
				func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if err := q.Resource.Validate(
						ctx,
						bytes.NewReader(data),
						int64(len(data)),
					); err != nil {
						t.Fatal(err)
					}
					return source.Result{
						Stream: &source.Stream{
							Body:           io.NopCloser(bytes.NewReader(data)),
							Status:         http.StatusOK,
							Representation: source.Representation{Size: int64(len(data))},
						},
						Used: func(context.Context) error { used++; return nil },
						Revalidate: func(context.Context) error {
							revalidated++
							if tc.deny {
								return source.ErrDenied
							}
							return nil
						},
					}, nil
				},
			)
			w := newItemWorld(
				t,
				false,
				func(c *Config) { c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read} },
			)
			c := repo.Defaults(
				&repo.Config{
					Key:            "peer-file",
					Class:          repo.Remote,
					PackageType:    repo.VCS,
					URL:            "https://peer.example/artifactory/api/vcs/source",
					VCSGitProvider: repo.ArtifactoryVCS,
				},
			)
			if err := w.db.Repos().Create(t.Context(), *c); err != nil {
				t.Fatal(err)
			}
			path := "/artifactory/api/vcs/downloadTagFile/peer-file/team/project/v1.0.0!README.md"
			if tc.failWrite {
				request := httptest.NewRequest(tc.method, path, nil)
				request.SetBasicAuth(tc.user, "pw")
				writer := &vcsFailedResponseWriter{header: make(http.Header)}
				w.h.ServeHTTP(writer, request)
				if writer.writes == 0 {
					t.Fatal("response did not reach failing writer")
				}
			} else {
				res := w.do(tc.method, path, tc.user, "")
				wantStatus(t, res, tc.status)
				if tc.status == http.StatusOK && !bytes.Equal(res.Body.Bytes(), data) {
					t.Fatal("peer file bytes changed")
				}
			}
			if used != tc.used {
				t.Fatalf("cache used %d times, want %d", used, tc.used)
			}
			if tc.method == http.MethodGet && tc.user == "reader" && revalidated < 1 {
				t.Fatalf("revalidated %d times", revalidated)
			}
		})
	}
}

type vcsFailedResponseWriter struct {
	header http.Header
	writes int
}

func (w *vcsFailedResponseWriter) Header() http.Header { return w.header }
func (w *vcsFailedResponseWriter) WriteHeader(int)     {}

func (w *vcsFailedResponseWriter) Write(
	[]byte,
) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

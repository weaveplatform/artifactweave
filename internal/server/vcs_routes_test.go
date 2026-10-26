package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/vcs"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func vcsPacket(line string) string { return fmt.Sprintf("%04x%s", len(line)+4, line) }

func vcsTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for _, file := range []struct{ name, body string }{
		{"project-012345/README.md", "module example\n"},
		{"project-012345/src/main.go", "package main\n"},
		{"project-012345/vendor/pkg/README.md", "vendored\n"},
	} {
		content := []byte(file.body)
		if err := w.WriteHeader(
			&tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(content))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAccVCSRemoteRESTAndSubgroup(t *testing.T) {
	const oid = "0123456789abcdef0123456789abcdef01234567"
	refs := []byte(vcsPacket("# service=git-upload-pack\n") + "0000" +
		vcsPacket(oid+" refs/heads/main\x00multi_ack\n") +
		vcsPacket(oid+" refs/heads/feature/one\n") +
		vcsPacket(oid+" refs/tags/v1.0.0\n") + "0000")
	archive := vcsTar(t)
	var calls []source.Request
	var used int
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
				calls = append(calls, q)
				if permit.Intent != source.MayFetch || !permit.UpstreamNetworkAllowed {
					t.Fatalf("VCS source permit: %+v", permit)
				}
				var data []byte
				switch {
				case strings.HasSuffix(q.Resource.ExternalURL, ".git/info/refs?service=git-upload-pack"):
					data = refs
					if q.Resource.ValidateMetadata == nil {
						t.Fatal("VCS refs lack metadata validation")
					}
					if err := q.Resource.ValidateMetadata(
						ctx,
						bytes.NewReader(data),
						int64(len(data)),
						source.MetadataResponse{},
					); err != nil {
						t.Fatalf("VCS refs validation: %v", err)
					}
				case strings.HasSuffix(q.Resource.ExternalURL, oid+".tar.gz"):
					data = archive
					if !q.Resource.NoCrossOriginAuth {
						t.Fatal("VCS archive could forward credentials across origins")
					}
					if q.Resource.Validate == nil {
						t.Fatal("VCS archive lacks integrity validation")
					}
					if err := q.Resource.Validate(
						ctx,
						bytes.NewReader(data),
						int64(len(data)),
					); err != nil {
						t.Fatalf("VCS archive validation: %v", err)
					}
				default:
					t.Fatalf("unexpected VCS upstream URL: %s", q.Resource.ExternalURL)
				}
				u, err := url.Parse(q.Resource.ExternalURL)
				if err != nil || q.Resource.CheckURL(ctx, u) != nil {
					t.Fatalf("VCS source URL policy rejected own URL: %v", err)
				}
				foreign, _ := url.Parse(
					"https://other.example/team/project/archive/" + oid + ".tar.gz",
				)
				if q.Resource.CheckURL(ctx, foreign) == nil {
					t.Fatal("VCS source URL policy admitted a foreign redirect")
				}
				return source.Result{
					Stream: &source.Stream{
						Body:   io.NopCloser(bytes.NewReader(data)),
						Status: http.StatusOK,
						Representation: source.Representation{
							Size:      int64(len(data)),
							MediaType: "application/octet-stream",
						},
					},
					Used: func(context.Context) error { used++; return nil },
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "vcs-remote", Class: repo.Remote, PackageType: repo.VCS,
		URL: "https://gitlab.example", VCSGitProvider: repo.GitLab,
	})); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/vcs"
	for _, path := range []string{
		base + "/tags/vcs-remote/team/project",
		base + "/tags/vcs-remote/team/subgroup/project",
	} {
		response := w.do(http.MethodGet, path, "reader", "")
		wantStatus(t, response, http.StatusOK)
		var listed []vcsListRef
		if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed) != 1 ||
			listed[0].Name != "v1.0.0" || listed[0].CommitID != oid {
			t.Fatalf("%s: %v %s", path, err, response.Body.String())
		}
	}
	for _, path := range []string{
		base + "/downloadTag/vcs-remote/team/project/v1.0.0",
		base + "/downloadTag/vcs-remote/team/subgroup/project/archive/refs/v1.0.0",
		base + "/downloadBranch/vcs-remote/team/project/archive/refs/feature/one",
		base + "/downloadBranch/vcs-remote/team/subgroup/project/archive/refs/feature/one",
	} {
		response := w.do(http.MethodGet, path, "reader", "")
		wantStatus(t, response, http.StatusOK)
		if !bytes.Equal(response.Body.Bytes(), archive) {
			t.Fatalf("%s altered provider archive", path)
		}
	}
	for _, tc := range []struct{ path, filename string }{
		{base + "/downloadTagFile/vcs-remote/team/project/v1.0.0!README.md", "README.md"},
		{base + "/downloadTagFile/vcs-remote/team/subgroup/project/v1.0.0!README.md", "README.md"},
		{base + "/downloadTagFile/vcs-remote/team/subgroup/project/archive/refs/v1.0.0!README.md", "README.md"},
		{base + "/downloadBranchFile/vcs-remote/team/project/archive/refs/feature/one!README.md", "README.md"},
		{base + "/downloadBranchFile/vcs-remote/team/subgroup/project/archive/refs/feature/one!README.md", "README.md"},
		{base + "/downloadTagFile/vcs-remote/team/project/v1.0.0!src/main.go", "src/main.go"},
		{base + "/downloadTagFile/vcs-remote/team/subgroup/project/archive/refs/v1.0.0!src/main.go", "src/main.go"},
	} {
		response := w.do(http.MethodGet, tc.path, "reader", "")
		wantStatus(t, response, http.StatusOK)
		z, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
		if err != nil || len(z.File) != 1 || z.File[0].Name != tc.filename {
			t.Fatalf("%s returned invalid selected-file ZIP: %v", tc.path, err)
		}
	}
	if curl, err := exec.LookPath("curl"); err == nil {
		clientServer := httptest.NewServer(w.h)
		defer clientServer.Close()
		cmd := exec.CommandContext(
			t.Context(),
			curl,
			"--fail-with-body",
			"--silent",
			"--show-error",
			"--user",
			"reader:pw",
			clientServer.URL+base+
				"/downloadTag/vcs-remote/team/subgroup/project/archive/refs/v1.0.0",
		)
		got, err := cmd.Output()
		if err != nil || !bytes.Equal(got, archive) {
			t.Fatalf("native curl archive request: %v", err)
		}
	} else {
		if os.Getenv("ARTIFACTWEAVE_REQUIRE_CURL") == "1" {
			t.Fatal("curl is required for VCS native acceptance")
		}
		t.Log("curl is unavailable; Go HTTP client assertions still run")
	}
	if len(calls) < 6 || used < 2 {
		t.Fatalf("VCS source/cache lifecycle calls=%d used=%d", len(calls), used)
	}
	for _, tc := range []struct {
		path, as string
		status   int
	}{
		{base + "/tags/vcs-remote/team/project", "user", http.StatusForbidden},
		{base + "/downloadTag/vcs-remote/team/project/v1.0.0", "user", http.StatusForbidden},
		{base + "/downloadTag/vcs-remote/team/subgroup/project/archive/refs/%2e%2e", "reader", http.StatusBadRequest},
		{base + "/downloadTag/vcs-remote/team/subgroup/project/archive/refs/v1.0.0?ext=exe", "reader", http.StatusBadRequest},
		{base + "/downloadBranch/vcs-remote/team/project/main?properties=qa%3Dapproved", "reader", http.StatusNotFound},
	} {
		response := w.do(http.MethodGet, tc.path, tc.as, "")
		if response.Code != tc.status {
			t.Fatalf(
				"%s as %s: %d %s, want %d",
				tc.path,
				tc.as,
				response.Code,
				response.Body.String(),
				tc.status,
			)
		}
	}
	if vcs.ValidExtension("exe") || vcs.ValidProject("team/../admin", "project") {
		t.Fatal("VCS path validation admitted escape")
	}
}

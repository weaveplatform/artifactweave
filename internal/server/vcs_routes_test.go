package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/vcs"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
				if permit.Intent != source.RetrievalIntentMayFetch ||
					!permit.UpstreamNetworkAllowed {
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

func TestUnitVCSArchiveRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, base, target, next string
		provider                 repo.VCSGitProvider
		allowed                  bool
	}{
		{"same url", "https://github.com", "https://github.com/team/project/archive/main.tar.gz", "https://github.com/team/project/archive/main.tar.gz", repo.GitHub, true},
		{"same origin archive", "https://git.example/git", "https://git.example/git/team/project/archive/main.tar.gz", "https://git.example/git/team/project/archive/other.tar.gz", repo.GitLab, true},
		{"github codeload", "https://github.com", "https://github.com/team/project/archive/main.tar.gz", "https://codeload.github.com/team/project/tar.gz/main", repo.GitHub, true},
		{"foreign host", "https://git.example", "https://git.example/team/project/archive/main.tar.gz", "https://evil.example/team/project/archive/main.tar.gz", repo.GitLab, false},
		{"sibling repository", "https://git.example", "https://git.example/team/project/archive/main.tar.gz", "https://git.example/team/project-other/archive/main.tar.gz", repo.GitLab, false},
		{"credential url", "https://git.example", "https://git.example/team/project/archive/main.tar.gz", "https://user:secret@git.example/team/project/archive/main.tar.gz", repo.GitLab, false},
		{"downgrade", "https://github.com", "https://github.com/team/project/archive/main.tar.gz", "http://codeload.github.com/team/project/tar.gz/main", repo.GitHub, false},
		{"non github codeload", "https://git.example", "https://git.example/team/project/archive/main.tar.gz", "https://codeload.github.com/team/project/tar.gz/main", repo.GitHub, false},
		{"fragment", "https://git.example", "https://git.example/team/project/archive/main.tar.gz", "https://git.example/team/project/archive/main.tar.gz#secret", repo.GitLab, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := url.Parse(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			next, err := url.Parse(tc.next)
			if err != nil {
				t.Fatal(err)
			}
			check := vcsArchiveURLPolicy(
				repo.Config{URL: tc.base, VCSGitProvider: tc.provider},
				"team",
				"project",
				target,
			)
			if got := check(t.Context(), next); (got == nil) != tc.allowed {
				t.Fatalf("check(%s) = %v; allowed=%t", next, got, tc.allowed)
			}
		})
	}
}

func TestUnitVCSPropertyFilterRejectsAmbiguousOrOversizedInput(t *testing.T) {
	for _, raw := range []string{"a=", "=b", "a=1&a=2", "a=1;b=2", "a=%", "a=1&b=2&c=3&d=4&e=5&f=6&g=7&h=8&i=9", strings.Repeat("a", 1025) + "=x"} {
		_, err := vcsPropertyFilter(raw)
		if err == nil {
			t.Errorf("accepted property filter %q", raw)
		}
	}
	for _, tc := range []struct {
		raw      string
		expected map[string]string
	}{
		{"", map[string]string{}},
		{"qa=approved&source=git", map[string]string{"qa": "approved", "source": "git"}},
	} {
		got, err := vcsPropertyFilter(tc.raw)
		if err != nil || len(got) != len(tc.expected) {
			t.Fatalf("filter %q = %v, %v", tc.raw, got, err)
		}
		for k, v := range tc.expected {
			if got[k] != v {
				t.Errorf("filter %q key %q = %q", tc.raw, k, got[k])
			}
		}
	}
}

func TestUnitVCSExactRefsURLDoesNotFollowRedirects(t *testing.T) {
	target, _ := url.Parse("https://git.example/team/project.git/info/refs?service=git-upload-pack")
	check := vcsExactURL(target)
	if err := check(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://git.example/team/other.git/info/refs?service=git-upload-pack",
		"https://evil.example/team/project.git/info/refs?service=git-upload-pack",
		"https://git.example/team/project.git/info/refs?service=git-receive-pack",
	} {
		next, _ := url.Parse(raw)
		if err := check(context.Background(), next); !errors.Is(err, source.ErrDenied) {
			t.Errorf("redirect %q: got %v, want denied", raw, err)
		}
	}
}

func TestUnitVCSArchiveBadRequestRejectsBeforeSource(t *testing.T) {
	s := artifactoryService{}
	for _, tc := range []struct{ kind, ext string }{{"tags", "exe"}, {"other", "zip"}} {
		_, _, err := s.vcsArchive(t.Context(), "r", "team", "project", tc.kind, "main", tc.ext, "")
		var api *apierror.Error
		if !errors.As(err, &api) || api.Status != http.StatusBadRequest {
			t.Errorf("kind=%s ext=%s: %v", tc.kind, tc.ext, err)
		}
	}
}

const vcsTestOID = "0123456789abcdef0123456789abcdef01234567"

func vcsTestRefs() []byte {
	return []byte(vcsPacket(vcsTestOID+" refs/heads/main\n") +
		vcsPacket(vcsTestOID+" refs/tags/v1.0.0\n") + "0000")
}

func vcsWorldWithSource(t *testing.T, read sourceBackend) *itemWorld {
	t.Helper()
	svc := &source.Service{Resolve: sourceIdentity, HTTP: read}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, c := range []repo.Config{
		{Key: "vcs-gitlab", Class: repo.Remote, PackageType: repo.VCS, URL: "https://gitlab.example", VCSGitProvider: repo.GitLab},
		{Key: "vcs-github", Class: repo.Remote, PackageType: repo.VCS, URL: "https://github.com", VCSGitProvider: repo.GitHub},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func vcsProviderData(
	t *testing.T,
	ctx context.Context,
	q source.Request,
	_ source.Permit,
) (source.Result, error) {
	t.Helper()
	var data []byte
	if strings.Contains(q.Resource.ExternalURL, "/info/refs?") {
		data = vcsTestRefs()
		if err := q.Resource.ValidateMetadata(
			ctx,
			bytes.NewReader(data),
			int64(len(data)),
			source.MetadataResponse{},
		); err != nil {
			t.Fatalf("valid references rejected: %v", err)
		}
	} else {
		data = vcsTar(t)
		if err := q.Resource.Validate(ctx, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("valid archive rejected: %v", err)
		}
	}
	return source.Result{Stream: &source.Stream{
		Body:   io.NopCloser(bytes.NewReader(data)),
		Status: http.StatusOK,
		Representation: source.Representation{
			Size:      int64(len(data)),
			MediaType: "application/octet-stream",
		},
	}}, nil
}

func TestAccVCSBranchRefCommitReleaseAndAlternativeDownloads(t *testing.T) {
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			if permit.Intent != source.RetrievalIntentMayFetch || !permit.UpstreamNetworkAllowed ||
				q.Subject.Name != "reader" {
				t.Fatalf("source call escaped read permit: %+v %+v", q.Subject, permit)
			}
			return vcsProviderData(t, ctx, q, permit)
		},
	)
	const base = "/artifactory/api/vcs/"
	for _, path := range []string{
		"branches/vcs-gitlab/team/project", "branches/vcs-gitlab/team/sub/project",
		"tags/vcs-gitlab/team/project", "tags/vcs-gitlab/team/sub/project",
	} {
		res := w.do(http.MethodGet, base+path, "reader", "")
		wantStatus(t, res, http.StatusOK)
		var got []vcsListRef
		if err := json.Unmarshal(
			res.Body.Bytes(),
			&got,
		); err != nil || len(got) != 1 ||
			got[0].CommitID != vcsTestOID {
			t.Fatalf("%s: refs %s, %v", path, res.Body.String(), err)
		}
	}
	for _, path := range []string{
		"refs/vcs-gitlab/team/project", "refs/vcs-gitlab/team/sub/project",
	} {
		res := w.do(http.MethodGet, base+path, "reader", "")
		wantStatus(t, res, http.StatusOK)
		var got string
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil ||
			!strings.Contains(got, vcsTestOID+" refs/heads/main") ||
			!strings.Contains(got, vcsTestOID+" refs/tags/v1.0.0") {
			t.Fatalf("%s: refs %s, %v", path, res.Body.String(), err)
		}
	}
	wantArchive := vcsTar(t)
	for _, path := range []string{
		"downloadCommit/vcs-gitlab/team/project/" + vcsTestOID,
		"downloadRelease/vcs-gitlab/team/project/v1.0.0",
		"downloadTag/vcs-gitlab/team/v1.0.0",
		"downloadBranch/vcs-gitlab/team/main",
		"downloadBranch/vcs-gitlab/team/project/main",
		"downloadTag/vcs-gitlab/team/project/v1.0.0",
		"downloadTag/vcs-gitlab/team/sub/project/archive/refs/v1.0.0",
	} {
		res := w.do(http.MethodGet, base+path, "reader", "")
		wantStatus(t, res, http.StatusOK)
		if !bytes.Equal(res.Body.Bytes(), wantArchive) {
			t.Fatalf("%s changed upstream archive", path)
		}
	}
	for _, path := range []string{
		"downloadTagFile/vcs-gitlab/team/v1.0.0!README.md",
		"downloadBranchFile/vcs-gitlab/team/main!README.md",
		"downloadBranchFile/vcs-gitlab/team/project/main!README.md",
		"downloadBranchFile/vcs-gitlab/team/sub/project/archive/refs/main!README.md",
	} {
		res := w.do(http.MethodGet, base+path, "reader", "")
		wantStatus(t, res, http.StatusOK)
		selected, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
		if err != nil || len(selected.File) != 1 || selected.File[0].Name != "README.md" {
			t.Fatalf("%s returned wrong selected ZIP: %v, files %v", path, err, selected)
		}
		body, err := selected.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil || closeErr != nil || string(data) != "module example\n" {
			t.Fatalf("%s selected file = %q, read %v, close %v", path, data, readErr, closeErr)
		}
	}
}

func TestAccVCSRepositoryAndNestedPathDenialsPrecedeUpstream(t *testing.T) {
	var calls int
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			calls++
			return vcsProviderData(t, ctx, q, permit)
		},
	)
	for _, tc := range []struct {
		path, as string
		status   int
	}{
		{"/artifactory/api/vcs/tags/vcs-gitlab/team/sub/project", "user", http.StatusForbidden},
		{"/artifactory/api/vcs/downloadTag/vcs-gitlab/team/sub/project/archive/refs/v1.0.0", "user", http.StatusForbidden},
		{"/artifactory/api/vcs/downloadTagFile/vcs-gitlab/team/sub/project/archive/refs/v1.0.0!README.md", "user", http.StatusForbidden},
		{"/artifactory/api/vcs/tags/missing/team/sub/project", "reader", http.StatusNotFound},
		{"/artifactory/api/vcs/tags/vcs-github/team/sub/project", "reader", http.StatusNotFound},
		{"/artifactory/api/vcs/downloadTag/vcs-gitlab/team/sub/project/archive/refs/%2e%2e", "reader", http.StatusBadRequest},
		{"/artifactory/api/vcs/downloadTagFile/vcs-gitlab/team/sub/project/archive/refs/v1.0.0!../secret", "reader", http.StatusBadRequest},
		{"/artifactory/api/vcs/downloadTag/vcs-gitlab/team/sub/project/archive/refs/v1.0.0?ext=exe", "reader", http.StatusBadRequest},
	} {
		res := w.do(http.MethodGet, tc.path, tc.as, "")
		if res.Code != tc.status {
			t.Errorf(
				"%s as %s = %d, want %d: %s",
				tc.path,
				tc.as,
				res.Code,
				tc.status,
				res.Body.String(),
			)
		}
	}
	if calls != 0 {
		t.Fatalf("denied requests reached upstream %d times", calls)
	}
}

func TestAccVCSUpstreamFailureIsSafeAcrossDocumentedRoutes(t *testing.T) {
	var calls int
	w := vcsWorldWithSource(
		t,
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			calls++
			if permit.Intent != source.RetrievalIntentMayFetch || !permit.UpstreamNetworkAllowed {
				t.Fatalf("unavailable source escaped read permit: %+v", permit)
			}
			return source.Result{}, source.ErrUnavailable
		},
	)
	for _, tc := range vcsTableCases {
		path := strings.Replace(tc.path, "vcs-remote", "vcs-gitlab", 1)
		res := w.do(http.MethodGet, "/artifactory/api/vcs/"+path, "reader", "")
		if res.Code != http.StatusBadGateway {
			t.Errorf("%s: status %d, want 502: %s", path, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "gitlab.example") {
			t.Errorf("%s: leaked source URL: %s", path, res.Body.String())
		}
	}
	if calls != len(vcsTableCases) {
		t.Fatalf("upstream calls = %d, want one per route (%d)", calls, len(vcsTableCases))
	}
}

func TestAccVCSNestedGrammarRejectsAmbiguousRoutes(t *testing.T) {
	var calls int
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			calls++
			return vcsProviderData(t, ctx, q, p)
		},
	)
	const base = "/artifactory/api/vcs/"
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"refs//team/sub/project", http.StatusBadRequest},
		{"refs/vcs-gitlab/team/sub/../project", http.StatusBadRequest},
		{"refs/vcs-gitlab/team/sub/.hidden", http.StatusBadRequest},
		{"other/vcs-gitlab/team/sub/project", http.StatusNotFound},
		{"downloadTag/vcs-gitlab/team/sub/project/v1.0.0", http.StatusNotFound},
		{"downloadTagFile/vcs-gitlab/team/sub/project/v1.0.0", http.StatusNotFound},
		{"downloadBranch/vcs-gitlab/team/sub/project/archive/refs/../main", http.StatusBadRequest},
		{"downloadBranchFile/vcs-gitlab/team/sub/project/archive/refs/main!", http.StatusBadRequest},
		{"downloadBranchFile/vcs-gitlab/team/sub/project/archive/refs/main!src%5csecret", http.StatusBadRequest},
	} {
		res := w.do(http.MethodGet, base+tc.path, "reader", "")
		if res.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", tc.path, res.Code, tc.status, res.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("rejected VCS paths reached upstream %d times", calls)
	}
}

func TestAccVCSUnknownRefAndMissingFileReturnNotFound(t *testing.T) {
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
			return vcsProviderData(t, ctx, q, p)
		},
	)
	for _, path := range []string{
		"downloadTag/vcs-gitlab/team/project/missing",
		"downloadBranch/vcs-gitlab/team/project/missing",
		"downloadTag/vcs-gitlab/team/sub/project/archive/refs/missing",
		"downloadTagFile/vcs-gitlab/team/project/v1.0.0!missing.go",
		"downloadBranchFile/vcs-gitlab/team/sub/project/archive/refs/main!missing.go",
	} {
		res := w.do(http.MethodGet, "/artifactory/api/vcs/"+path, "reader", "")
		if res.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %s", path, res.Code, res.Body.String())
		}
	}
}

func TestAccVCSMalformedUpstreamRefsAndFileArchiveFailClosed(t *testing.T) {
	malformedRefs := true
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			var data []byte
			if strings.Contains(q.Resource.ExternalURL, "/info/refs?") {
				if malformedRefs {
					data = []byte("not a Git packet line")
					if err := q.Resource.ValidateMetadata(
						ctx,
						bytes.NewReader(data),
						int64(len(data)),
						source.MetadataResponse{},
					); !errors.Is(
						err,
						source.ErrInvalidResult,
					) {
						t.Fatalf("malformed refs validator = %v", err)
					}
				} else {
					data = vcsTestRefs()
				}
			} else {
				data = []byte("not a gzip archive")
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(data),
					int64(len(data)),
				); !errors.Is(
					err,
					source.ErrInvalidResult,
				) {
					t.Fatalf("malformed archive validator = %v", err)
				}
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(data)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(data))},
			}}, nil
		},
	)
	refs := w.do(http.MethodGet, "/artifactory/api/vcs/tags/vcs-gitlab/team/project", "reader", "")
	wantStatus(t, refs, http.StatusBadGateway)
	if strings.Contains(refs.Body.String(), "not a Git packet") {
		t.Fatal("malformed upstream refs leaked to caller")
	}
	malformedRefs = false
	file := w.do(
		http.MethodGet,
		"/artifactory/api/vcs/downloadTagFile/vcs-gitlab/team/project/v1.0.0!README.md",
		"reader",
		"",
	)
	wantStatus(t, file, http.StatusBadGateway)
	if strings.Contains(file.Body.String(), "not a gzip") {
		t.Fatal("malformed archive leaked to caller")
	}
}

func TestAccVCSPropertyConstrainedArchiveDoesNotFetchUpstream(t *testing.T) {
	var refsCalls, archiveCalls int
	w := vcsWorldWithSource(
		t,
		func(ctx context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			if strings.Contains(q.Resource.ExternalURL, "/info/refs?") {
				refsCalls++
				return vcsProviderData(t, ctx, q, permit)
			}
			archiveCalls++
			return vcsProviderData(t, ctx, q, permit)
		},
	)
	res := w.do(http.MethodGet,
		"/artifactory/api/vcs/downloadBranch/vcs-gitlab/team/project/main?properties=qa%3Dapproved",
		"reader", "")
	wantStatus(t, res, http.StatusNotFound)
	if refsCalls != 1 || archiveCalls != 0 {
		t.Fatalf(
			"property-constrained source calls: refs=%d archive=%d; archive fetch must be suppressed",
			refsCalls,
			archiveCalls,
		)
	}
}

func TestUnitVCSResultStreamRevalidationAndUsage(t *testing.T) {
	s := artifactoryService{}
	var used, closed int
	newBody := func(body string) *source.Stream {
		return &source.Stream{
			Body: &countingVCSReadCloser{
				Reader: strings.NewReader(body),
				closed: &closed,
			},
			Representation: source.Representation{Size: int64(len(body))},
		}
	}
	result := source.Result{
		Stream: newBody("archive"),
		Used:   func(context.Context) error { used++; return nil },
	}
	body, size, err := s.vcsOpenResult(t.Context(), result)
	if err != nil || size != 7 {
		t.Fatalf("open = size %d, err %v", size, err)
	}
	if data, err := io.ReadAll(body); err != nil || string(data) != "archive" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if used != 1 {
		t.Fatalf("used callback = %d, want 1 after EOF", used)
	}
	if err := body.Close(); err != nil || closed != 1 {
		t.Fatalf("close = %v, count %d", err, closed)
	}
	result = source.Result{
		Stream: newBody("partial"),
		Used:   func(context.Context) error { used++; return nil },
	}
	partial, _, err := s.vcsOpenResult(t.Context(), result)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := partial.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil || used != 1 {
		t.Fatalf("partial close = %v, used %d; incomplete stream must not count as used", err, used)
	}
	result = source.Result{Stream: newBody("unknown")}
	result.Stream.Size = -1
	unknown, size, err := s.vcsOpenResult(t.Context(), result)
	if err != nil || size != 0 {
		t.Fatalf("unknown-length source = size %d, err %v", size, err)
	}
	if err := unknown.Close(); err != nil {
		t.Fatal(err)
	}
	result = source.Result{
		Stream:     newBody("bad"),
		Revalidate: func(context.Context) error { return source.ErrDenied },
	}
	var denied *apierror.Error
	if _, _, err := s.vcsOpenResult(
		t.Context(),
		result,
	); !errors.As(err, &denied) || denied.Status != http.StatusForbidden ||
		closed != 4 {
		t.Fatalf("revoked source = %v, closed %d", err, closed)
	}
	if _, _, err := s.vcsOpenResult(
		t.Context(),
		source.Result{},
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("missing source = %v", err)
	}
	result = source.Result{
		Stream:     newBody("refs"),
		Revalidate: func(context.Context) error { return source.ErrDenied },
	}
	denied = nil
	if _, err := s.vcsResultBytes(
		t.Context(),
		result,
		8,
	); !errors.As(err, &denied) ||
		denied.Status != http.StatusForbidden {
		t.Fatalf("revoked refs = %v", err)
	}
	result = source.Result{Stream: newBody("oversized")}
	if _, err := s.vcsResultBytes(
		t.Context(),
		result,
		3,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("oversized refs = %v", err)
	}
}

type countingVCSReadCloser struct {
	io.Reader
	closed *int
}

func (r *countingVCSReadCloser) Close() error { *r.closed++; return nil }

func TestUnitVCSPropertyFilterRequiresCachedItem(t *testing.T) {
	s := artifactoryService{}
	closed := 0
	result := source.Result{
		Stream: &source.Stream{
			Body: &countingVCSReadCloser{Reader: strings.NewReader("stream"), closed: &closed},
		},
	}
	err := s.vcsMatchCachedProperties(t.Context(), result, map[string]string{"qa": "approved"})
	var api *apierror.Error
	if !errors.As(err, &api) || api.Status != http.StatusNotFound || closed != 1 {
		t.Fatalf("uncached filtered source = %v, closed %d", err, closed)
	}
}

func TestUnitVCSPropertyFilterMatchesOnlyCachedItemProperties(t *testing.T) {
	w := newItemWorld(t, false)
	item, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "up", Path: "team", Name: "project.tar.gz", Cached: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().SetProperties(t.Context(), item.ID,
		types.Properties{"qa": {"approved"}, "source": {"git"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s := artifactoryService{cfg: Config{Store: w.db}}
	result := source.Result{Item: &item}
	for _, tc := range []struct {
		name   string
		filter map[string]string
		found  bool
	}{
		{"matching two properties", map[string]string{"qa": "approved", "source": "git"}, true},
		{"wrong value", map[string]string{"qa": "rejected"}, false},
		{"missing key", map[string]string{"owner": "team"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.vcsMatchCachedProperties(t.Context(), result, tc.filter)
			if tc.found && err != nil {
				t.Fatalf("matching cached properties rejected: %v", err)
			}
			if !tc.found {
				var api *apierror.Error
				if !errors.As(err, &api) || api.Status != http.StatusNotFound {
					t.Fatalf("unmatched properties = %v, want 404", err)
				}
			}
		})
	}
}

func TestUnitVCSCachedResultRequiresBlobAndPreservesReadFault(t *testing.T) {
	result := source.Result{Item: &types.Item{Size: 7}}
	s := artifactoryService{}
	if _, _, err := s.vcsOpenResult(
		t.Context(),
		result,
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Fatalf("cached archive without blob store = %v", err)
	}
	if _, err := s.vcsResultBytes(
		t.Context(),
		result,
		8,
	); !errors.Is(
		err,
		apierror.ErrNotImplemented,
	) {
		t.Fatalf("cached refs without blob store = %v", err)
	}
	w := newItemWorld(t, false)
	s.cfg.Blobs = w.blobs
	for _, read := range []func() error{
		func() error { _, _, err := s.vcsOpenResult(t.Context(), result); return err },
		func() error { _, err := s.vcsResultBytes(t.Context(), result, 8); return err },
	} {
		var api *apierror.Error
		if err := read(); !errors.As(err, &api) || api.Status != http.StatusInternalServerError {
			t.Fatalf("missing cached blob = %v, want sanitized 500", err)
		}
	}
}

func TestUnitVCSArchiveURLPolicyMalformedBaseFallsBackToExactURL(t *testing.T) {
	target, _ := url.Parse("https://git.example/team/project/archive/main.tar.gz")
	check := vcsArchiveURLPolicy(repo.Config{URL: "%"}, "team", "project", target)
	if err := check(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	next, _ := url.Parse("https://git.example/team/project/archive/other.tar.gz")
	if err := check(t.Context(), next); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("malformed configured base accepted redirect: %v", err)
	}
	if !vcs.ValidCommitID(vcsTestOID) {
		t.Fatal("test commit ID is invalid")
	}
}

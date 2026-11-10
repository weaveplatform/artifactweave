package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	vcshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/vcs/http"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

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
	s := newArtifactoryService(Config{})
	for _, tc := range []struct{ kind, ext string }{{"tags", "exe"}, {"other", "zip"}} {
		_, _, err := s.vcsArchive(t.Context(), "r", "team", "project", tc.kind, "main", tc.ext, "")
		var api *apierror.Error
		if !errors.As(err, &api) || api.Status != http.StatusBadRequest {
			t.Errorf("kind=%s ext=%s: %v", tc.kind, tc.ext, err)
		}
	}
}

const vcsTestOID = "0123456789abcdef0123456789abcdef01234567"

func TestUnitVCSResultStreamRevalidationAndUsage(t *testing.T) {
	s := newArtifactoryService(Config{})
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
	s := newArtifactoryService(Config{})
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
	s := newArtifactoryService(Config{Store: w.db})
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
	s := newArtifactoryService(Config{})
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
	if !vcshttp.ValidCommitID(vcsTestOID) {
		t.Fatal("test commit ID is invalid")
	}
}

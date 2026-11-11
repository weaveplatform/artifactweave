package source_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// policyService answers every authorized read with an empty document.
func policyService() *source.Service {
	return &source.Service{
		Resolve: identity,
		HTTP: backend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
			return stream(io.NopCloser(strings.NewReader("{}"))), nil
		}),
	}
}

// packument is a metadata read of npm's packument for pkg, authorized at
// the folder pkg and checked by the policy at 7.x's cache path.
func packument(t *testing.T, scope string) source.Request {
	t.Helper()
	q := request(t, scope)
	q.Resource.Kind, q.Resource.Path, q.Resource.EscapedPath =
		source.ResourceKindMetadata, "packument", "packument"
	q.Resource.PermissionFolder, q.Resource.PermissionPath = true, "pkg"
	q.Resource.PolicyPath = ".npm/pkg/package.json"
	return q
}

// wantRefused checks that err is the policy's 404 with message.
func wantRefused(t *testing.T, err error, message string) {
	t.Helper()
	var rejection *format.RejectionError
	if !errors.As(err, &rejection) || rejection.Status != http.StatusNotFound ||
		rejection.Message != message {
		t.Fatalf("got %v, want the policy's 404 %q", err, message)
	}
}

// A read that names a PolicyPath is refused by the patterns at that path,
// where 7.x caches the document, and not at the folder its permission is
// checked at; Read permission is still checked at the folder.
func TestUnitPolicyPathSeparatesPolicyFromPermission(t *testing.T) {
	svc := policyService()
	q := packument(t, "")
	q.Repo.ExcludesPattern = new(".npm/**")
	_, err := svc.Read(t.Context(), q)
	wantRefused(t, err, "The repository 'up' rejected the artifact 'up:.npm/pkg/package.json' "+
		"due to its include/exclude pattern settings.")
	q.Repo.ExcludesPattern = new("pkg/**")
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatalf("a pattern on the permission folder refused the document: %v", err)
	}
	_ = result.Stream.Body.Close()
	q.Repo.ExcludesPattern = nil
	q.Repo.BlackedOut = new(true)
	_, err = svc.Read(t.Context(), q)
	wantRefused(t, err, "The repository 'up' is blacked out and cannot serve artifact "+
		"'up:.npm/pkg/package.json'.")
	q = packument(t, "applied-permissions/user artifact:.npm:r")
	if _, err = svc.Read(t.Context(), q); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("the policy path bypassed the permission folder: %v", err)
	}
	for _, invalid := range []string{"/abs", "a//b", "a\nb", "../escape", "a/"} {
		q = packument(t, "")
		q.Resource.PolicyPath = invalid
		if _, err = svc.Read(t.Context(), q); !errors.Is(err, source.ErrInvalidRequest) {
			t.Fatalf("policy path %q: %v", invalid, err)
		}
	}
}

// BlackoutOnly refuses only a blacked-out repository's read: the patterns
// do not refuse a lookup a download makes on its way to its file.
func TestUnitPolicyBlackoutOnly(t *testing.T) {
	svc := policyService()
	q := packument(t, "")
	q.Resource.BlackoutOnly = true
	q.Repo.ExcludesPattern = new("**")
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatalf("the patterns refused a blackout-only read: %v", err)
	}
	_ = result.Stream.Body.Close()
	q.Repo.BlackedOut = new(true)
	_, err = svc.Read(t.Context(), q)
	wantRefused(t, err, "The repository 'up' is blacked out and cannot serve artifact "+
		"'up:.npm/pkg/package.json'.")
	q.Resource.PolicyPath = ""
	_, err = svc.Read(t.Context(), q)
	wantRefused(t, err, "The repository 'up' is blacked out and cannot serve artifact 'up:pkg'.")
}

// CheckPolicy is the rule Service.Read applies and the application's
// Revalidate fences a result with.
func TestUnitCheckPolicy(t *testing.T) {
	c := repo.Config{Key: "up", Class: repo.Remote, ExcludesPattern: new("pkg/**")}
	folder := source.Resource{PermissionFolder: true}
	if err := folder.CheckPolicy(c, "pkg"); err == nil {
		t.Fatal("the folder's patterns accepted it")
	}
	folder.PolicyPath = ".npm/pkg/package.json"
	if err := folder.CheckPolicy(c, "pkg"); err != nil {
		t.Fatalf("the permission folder was checked beside the policy path: %v", err)
	}
	c.ExcludesPattern = new(".npm/**")
	if err := folder.CheckPolicy(c, "pkg"); err == nil {
		t.Fatal("the policy path's patterns accepted it")
	}
	folder.BlackoutOnly = true
	if err := folder.CheckPolicy(c, "pkg"); err != nil {
		t.Fatalf("a blackout-only check applied the patterns: %v", err)
	}
}

// CheckRead hands a disclosed path's policy refusal back as the policy's
// own answer, so a listing can keep a release whose file is refused and a
// read answers the policy's 404; a path the caller may not read is still
// ErrDenied.
func TestUnitCheckReadKeepsPolicyRefusal(t *testing.T) {
	svc := policyService()
	q := packument(t, "")
	q.Resource.PolicyPath = ""
	q.Repo.ExcludesPattern = new("pkg/-/pkg-1.0.0.tgz")
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	err = result.CheckRead(t.Context(), "pkg/-/pkg-1.0.0.tgz")
	wantRefused(t, err, "The repository 'up' rejected the artifact 'up:pkg/-/pkg-1.0.0.tgz' "+
		"due to its include/exclude pattern settings.")
	if errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("a policy refusal is a gateway failure: %v", err)
	}
	if err = result.CheckRead(t.Context(), "pkg/-/pkg-2.0.0.tgz"); err != nil {
		t.Fatalf("an accepted release: %v", err)
	}
	if err = result.CheckRead(t.Context(), "other/x.tgz"); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("a path outside the folder: %v", err)
	}
}

// CheckPermission is CheckRead without the policy: a listing keeps a
// release whose file the patterns reject, which CheckRead refuses, while a
// release the caller may not read and a path outside the folder are still
// ErrDenied.
func TestUnitCheckPermissionLeavesPolicyOut(t *testing.T) {
	svc := policyService()
	q := packument(t, "artifact:up/pkg/-/pkg-1.0.0.tgz:r")
	q.Resource.PolicyPath = ""
	q.Repo.ExcludesPattern = new("pkg/-/pkg-1.0.0.tgz")
	result, err := svc.Read(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Stream.Body.Close()
	if err = result.CheckPermission(t.Context(), "pkg/-/pkg-1.0.0.tgz"); err != nil {
		t.Fatalf("the patterns refused a listed release: %v", err)
	}
	err = result.CheckRead(t.Context(), "pkg/-/pkg-1.0.0.tgz")
	wantRefused(t, err, "The repository 'up' rejected the artifact 'up:pkg/-/pkg-1.0.0.tgz' "+
		"due to its include/exclude pattern settings.")
	for _, path := range []string{"pkg/-/pkg-2.0.0.tgz", "other/x.tgz"} {
		if err = result.CheckPermission(t.Context(), path); !errors.Is(err, source.ErrDenied) {
			t.Fatalf("%s: %v, want ErrDenied", path, err)
		}
	}
}

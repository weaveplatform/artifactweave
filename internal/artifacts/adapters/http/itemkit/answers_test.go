package itemkit

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// wantAnswer fails t unless err is the apierror of status and message.
func wantAnswer(t *testing.T, err error, status int, message string) {
	t.Helper()
	var got *apierror.Error
	if !errors.As(err, &got) || got.Status != status || got.Message != message {
		t.Fatalf("answered %v, want %d %q", err, status, message)
	}
}

// TestUnitDownloadAnswersNameThePath (#1572): a file request's miss and
// refusal carry 7.161.26's reasons and the path of the file an archive
// entry or a checksum names, under the key the request named; a folder
// request and a virtual repository keep ItemNotFound and httpkit.Denied.
func TestUnitDownloadAnswersNameThePath(t *testing.T) {
	local := repo.Config{Key: "libs-local", Class: repo.Local}
	remote := repo.Config{Key: "up", Class: repo.Remote}
	offline := repo.Config{Key: "up", Class: repo.Remote, Offline: new(true)}
	virtual := repo.Config{Key: "virt", Class: repo.Virtual}
	file := func(key, p string) ItemRequest { return ItemRequest{Key: key, Path: p} }
	for _, tc := range []struct {
		name    string
		c       repo.Config
		cached  bool
		req     ItemRequest
		message string
	}{
		{"local", local, false, file("libs-local", "a/f.bin"), "File not found.; Path: 'libs-local:a/f.bin'"},
		{"checksum", local, false, file("libs-local", "a/f.bin.md5"), "File not found.; Path: 'libs-local:a/f.bin'"},
		{"archive", local, false, file("libs-local", "a/f.zip!/x"), "File not found.; Path: 'libs-local:a/f.zip'"},
		{"remote", remote, false, file("up", "f.bin"), "Not Found; Path: 'up:f.bin'"},
		{
			"offline", offline, false, file("up", "f.bin"),
			"up: is offline, 'up:f.bin' is not found at 'f.bin'.; Path: 'up:f.bin'",
		},
		{"cache", offline, true, file("up-cache", "f.bin"), "File not found.; Path: 'up-cache:f.bin'"},
		{"folder", local, false, ItemRequest{Key: "libs-local", Path: "a", IsFolder: true}, "Could not find resource"},
		{"virtual", virtual, false, file("virt", "a/f.bin"), "Could not find resource"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantAnswer(t, missed(tc.c, tc.cached, tc.req), http.StatusNotFound, tc.message)
		})
	}

	user := kernel.Principal{Name: "user"}
	anonymous := kernel.Principal{Name: "anonymous", Anonymous: true}
	wantAnswer(t, refused(local, file("libs-local", "a/f.bin.sha1"), user), http.StatusForbidden,
		"Download request for repo:path 'libs-local:a/f.bin' is forbidden for user: 'user'.; "+
			"Path: 'libs-local:a/f.bin'")
	wantAnswer(
		t,
		refused(remote, file("up", "f.bin"), user),
		http.StatusForbidden,
		"Download request for repo:path 'up-cache:f.bin' is forbidden for user: 'user'.; Path: 'up:f.bin'",
	)
	wantAnswer(t, refused(local, file("libs-local", "a/f.bin"), anonymous), http.StatusUnauthorized,
		"Unauthorized")
	if err := refused(
		virtual,
		file("virt", "a/f.bin"),
		user,
	); !errors.Is(
		err,
		kernel.ErrForbidden,
	) {
		t.Fatal("virtual", err)
	}
	folder := ItemRequest{Key: "libs-local", Path: "a", IsFolder: true}
	if err := refused(local, folder, anonymous); !errors.Is(err, kernel.ErrUnauthenticated) {
		t.Fatal("folder", err)
	}
	// A checksum of a file the caller may not read is refused as the file
	// is, and a virtual repository still hides the file behind its 404.
	wantAnswer(
		t,
		checksumRefused(local, file("libs-local", "a/f.bin.md5"), user),
		http.StatusForbidden,
		"Download request for repo:path 'libs-local:a/f.bin' is forbidden for user: 'user'.; "+
			"Path: 'libs-local:a/f.bin'",
	)
	wantAnswer(t, checksumRefused(virtual, file("virt", "a/f.bin.md5"), user), http.StatusNotFound,
		"Could not find resource")
}

// TestUnitSourceAnswerKeepsTheSourcesOtherAnswers (#1572): a remote
// source's miss and want of Read take the download's answers, and every
// failure pkgroute.SourceError tests before them keeps its answer, the
// download policy's refusal, which is kernel.ErrNotFound too, among them.
func TestUnitSourceAnswerKeepsTheSourcesOtherAnswers(t *testing.T) {
	remote := repo.Config{Key: "up", Class: repo.Remote}
	req := ItemRequest{Key: "up", Path: "f.bin"}
	user := kernel.Principal{Name: "user"}
	wantAnswer(t, sourceAnswer(remote, req, user, fmt.Errorf("read: %w", kernel.ErrNotFound)),
		http.StatusNotFound, "Not Found; Path: 'up:f.bin'")
	wantAnswer(
		t,
		sourceAnswer(remote, ItemRequest{Key: "up-cache", Path: "f.bin"}, user, kernel.ErrNotFound),
		http.StatusNotFound,
		"File not found.; Path: 'up-cache:f.bin'",
	)
	wantAnswer(
		t,
		sourceAnswer(remote, req, user, source.ErrDenied),
		http.StatusForbidden,
		"Download request for repo:path 'up-cache:f.bin' is forbidden for user: 'user'.; Path: 'up:f.bin'",
	)
	policy := errors.Join(
		kernel.ErrNotFound,
		format.Rejectf(http.StatusNotFound, "refused by policy"),
	)
	wantAnswer(t, sourceAnswer(remote, req, user, policy), http.StatusNotFound, "refused by policy")
	upstream := errors.Join(kernel.ErrNotFound, &source.HTTPError{Status: http.StatusBadGateway})
	wantAnswer(t, sourceAnswer(remote, req, user, upstream), http.StatusBadGateway, "Bad Gateway")
	invalid := errors.Join(kernel.ErrNotFound, source.ErrInvalidRequest)
	wantAnswer(t, sourceAnswer(remote, req, user, invalid), http.StatusBadRequest, "Bad Request")
	fault := errors.Join(kernel.ErrNotFound, format.ErrStorageFault)
	if err := sourceAnswer(remote, req, user, fault); !errors.Is(err, format.ErrStorageFault) {
		t.Fatal("storage fault", err)
	}
	// A virtual repository's read of a member keeps SourceError's answers.
	member := ItemRequest{Key: "up", Path: "f.bin", member: true}
	wantAnswer(
		t,
		sourceAnswer(remote, member, user, kernel.ErrNotFound),
		http.StatusNotFound,
		"Not Found",
	)
}

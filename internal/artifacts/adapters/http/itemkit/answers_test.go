package itemkit

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
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

// TestUnitOverlayAnswersAreTheDownloads (#1599): the answers a format
// overlay writes on the item route are the download's: Missed, Refused and
// Mismatched are missed, refused and mismatched; ReadAnswer gives a failed
// read's miss and want of Read those answers, whether the failure is the
// source's own or pkgroute.SourceError's answer to it, gives a cache fill
// the caller may not make and an assumed-offline miss the generic route's
// answers (remoteMissAnswer, #1611), and keeps every
// other failure, ErrFolderAtPath, the upstream's own status and the
// policy's refusal among them; NotServed refuses a caller without Read,
// then answers the download policy, then the miss. A folder request and a
// virtual repository keep their answers.
func TestUnitOverlayAnswersAreTheDownloads(t *testing.T) {
	local := repo.Config{Key: "deb-local", Class: repo.Local}
	remote := repo.Config{Key: "up", Class: repo.Remote}
	virtual := repo.Config{Key: "virt", Class: repo.Virtual}
	req := ItemRequest{Key: "deb-local", Path: "dists/Packages"}
	remoteReq := ItemRequest{Key: "up", Path: "dists/Packages"}
	folder := ItemRequest{Key: "deb-local", Path: "dists", IsFolder: true}
	user := kernel.Principal{Name: "user"}
	anonymous := kernel.Principal{Name: "anonymous", Anonymous: true}
	localMiss := "File not found.; Path: 'deb-local:dists/Packages'"
	localRefusal := "Download request for repo:path 'deb-local:dists/Packages' is forbidden " +
		"for user: 'user'.; Path: 'deb-local:dists/Packages'"

	wantAnswer(t, Missed(local, false, req), http.StatusNotFound, localMiss)
	wantAnswer(t, Refused(local, req, user), http.StatusForbidden, localRefusal)
	wantAnswer(t, Refused(local, req, anonymous), http.StatusUnauthorized, "Unauthorized")
	wantAnswer(t, Mismatched(remote, remoteReq), http.StatusNotFound,
		"File 'up-cache:dists/Packages' was found, but mandatory properties do not match.; "+
			"Path: 'up-cache:dists/Packages'")
	wantAnswer(t, Missed(virtual, false, ItemRequest{Key: "virt", Path: "a"}), http.StatusNotFound,
		"Could not find resource")
	if err := Refused(local, folder, user); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("folder refusal", err)
	}

	notFound := apierror.New(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	upstream404 := apierror.New(http.StatusNotFound, http.StatusText(http.StatusNotFound))
	upstream404.NoChallenge = true
	policy := errors.Join(
		kernel.ErrNotFound,
		format.Rejectf(http.StatusNotFound, "refused by policy"),
	)
	for _, tc := range []struct {
		name    string
		c       repo.Config
		req     ItemRequest
		caller  kernel.Principal
		err     error
		status  int
		message string
	}{
		{"source miss", local, req, user, fmt.Errorf("read: %w", kernel.ErrNotFound), 404, localMiss},
		{"SourceError's miss", remote, remoteReq, user, notFound, 404, "Not Found; Path: 'up:dists/Packages'"},
		{"source refusal", local, req, user, source.ErrDenied, 403, localRefusal},
		{"SourceError's refusal", local, req, user, kernel.ErrForbidden, 403, localRefusal},
		{"SourceError's anonymous refusal", local, req, anonymous, kernel.ErrUnauthenticated, 401, "Unauthorized"},
		{"upstream's own 404", remote, remoteReq, user, upstream404, 404, "Not Found"},
		{"virtual miss", virtual, ItemRequest{Key: "virt", Path: "a"}, user, notFound, 404, "Not Found"},
		{
			"cache fill refused", remote, remoteReq, user, &source.CacheFillDenied{User: "user", Repo: "up", Path: "dists/Packages"}, 403,
			"Rejected artifact download request: User user is not permitted to deploy " +
				"'dists/Packages' into 'up-cache:dists/Packages'.",
		},
		{"cache fill refused anonymously", remote, remoteReq, anonymous, &source.CacheFillDenied{User: "user", Repo: "up", Path: "dists/Packages"}, 401, "Unauthorized"},
		{
			"assumed offline", remote, remoteReq, user, source.ErrAssumedOffline, 404,
			"up: is assumed offline, 'up:dists/Packages' is not found at 'dists/Packages'.; " +
				"Path: 'up:dists/Packages'",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantAnswer(t, ReadAnswer(tc.c, false, tc.req, tc.caller, tc.err), tc.status, tc.message)
		})
	}
	for name, err := range map[string]error{
		"nil":          nil,
		"folder":       ErrFolderAtPath,
		"storage":      format.ErrStorageFault,
		"upstream":     &source.HTTPError{Status: http.StatusBadGateway},
		"invalid":      source.ErrInvalidRequest,
		"unsupported":  source.ErrUnsupported,
		"other":        errors.New("other"),
		"folder miss":  kernel.ErrNotFound,
		"denied again": source.ErrDenied,
		"policy":       policy,
	} {
		r := req
		if strings.HasPrefix(name, "folder ") || name == "denied again" {
			r = folder
		}
		if got := ReadAnswer(
			local,
			false,
			r,
			user,
			err,
		); got != err { //nolint:errorlint // the same error
			t.Errorf("%s: answered %v, want it kept", name, got)
		}
	}

	allow := Access{Decider: &accessapitest.Decider{Admin: true}}
	deny := Access{Decider: &accessapitest.Decider{}}
	wantAnswer(t, NotServed(local, false, req, user, allow), http.StatusNotFound, localMiss)
	wantAnswer(t, NotServed(remote, false, remoteReq, user, allow), http.StatusNotFound,
		"Not Found; Path: 'up:dists/Packages'")
	wantAnswer(t, NotServed(local, false, req, user, deny), http.StatusForbidden, localRefusal)
	wantAnswer(
		t,
		NotServed(local, false, req, anonymous, deny),
		http.StatusUnauthorized,
		"Unauthorized",
	)
	dark := repo.Config{Key: "deb-local", Class: repo.Local, BlackedOut: new(true)}
	wantAnswer(
		t,
		NotServed(dark, false, req, user, allow),
		http.StatusNotFound,
		"The repository 'deb-local' is blacked out and cannot serve artifact 'deb-local:dists/Packages'.",
	)
	wantAnswer(t, NotServed(local, false, folder, user, deny), http.StatusNotFound,
		"Could not find resource")
}

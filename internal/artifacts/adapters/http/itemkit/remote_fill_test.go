package itemkit

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestUnitRemoteMissAnswers (#1587, #1601): a file request's refused cache
// fill answers 7.x's download rejection, 403 without a path suffix, or 401
// for an anonymous caller; an assumed-offline miss answers 404 with
// internalGetInfo's reason for the file a checksum names; a folder request
// leaves both to SourceError, which answers them as misses.
func TestUnitRemoteMissAnswers(t *testing.T) {
	remote := repo.Config{Key: "up", Class: repo.Remote}
	user := kernel.Principal{Name: "reader"}
	anonymous := kernel.Principal{Name: "anonymous", Anonymous: true}
	file := ItemRequest{Key: "up", Path: "a/f.bin"}
	denied := fmt.Errorf("read source: %w",
		&source.CacheFillDenied{User: "reader", Repo: "up", Path: "a/f.bin"})

	wantAnswer(t, sourceAnswer(remote, file, user, denied), http.StatusForbidden,
		"Rejected artifact download request: User reader is not permitted to deploy "+
			"'a/f.bin' into 'up-cache:a/f.bin'.")
	answer := sourceAnswer(remote, file, anonymous, denied)
	wantAnswer(t, answer, http.StatusUnauthorized, "Unauthorized")
	var e *apierror.Error
	if errors.As(answer, &e) && e.NoChallenge {
		t.Fatal("anonymous refusal without its challenge")
	}
	wantAnswer(t, sourceAnswer(remote, ItemRequest{Key: "up", Path: "a/f.bin.sha1"}, user,
		source.ErrAssumedOffline), http.StatusNotFound,
		"up: is assumed offline, 'up:a/f.bin' is not found at 'a/f.bin'.; Path: 'up:a/f.bin'")

	folder := ItemRequest{Key: "up", Path: "a", IsFolder: true}
	for _, err := range []error{denied, source.ErrAssumedOffline} {
		if remoteMissAnswer(remote, folder, user, err) != nil {
			t.Fatalf("folder request answered %v here", err)
		}
		wantAnswer(t, sourceAnswer(remote, folder, user, err), http.StatusNotFound,
			http.StatusText(http.StatusNotFound))
	}
	if remoteMissAnswer(remote, file, user, kernel.ErrNotFound) != nil {
		t.Fatal("a plain miss answered here")
	}
}

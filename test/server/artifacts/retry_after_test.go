package artifacts_test

// This file holds the suites of #1415: an operation's own 503 that reports
// a transient condition carries Retry-After, as decision 0043's "Rate
// headers and Retry-After" asks, with the status and body it had before.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// TestAccCollectedDeployHintsARetry: a deploy whose content garbage
// collection is removing answers 503 with its message and Retry-After: 1,
// and the repeat it asks for stores the file. The collector here marked the
// blob and stopped, which leaves it deleting, as a collection running beside
// the deploy does.
func TestAccCollectedDeployHintsARetry(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := t.Context()
	if err := w.db.Settings().Put(ctx, settings.SettingTrashcan,
		artmodel.TrashcanSettings{Enabled: false, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	const body = "content a collection removes while it is deployed"
	sum := sha256.Sum256([]byte(body))
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/a.txt", "admin", body), 201)
	wantStatus(t, w.do(http.MethodDelete, "/artifactory/libs-local/a.txt", "admin", ""), 204)
	if _, err := w.db.Blobs().MarkDeleting(ctx, blob.Blob{
		Checksums: kernel.Checksums{SHA256: hex.EncodeToString(sum[:])},
		Size:      int64(len(body)),
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodPut, "/artifactory/libs-local/b.txt", "admin", body)
	// The message is the error's, as before #1415: ErrCollected, then the
	// store's reason.
	var got struct {
		Errors []struct {
			Message string `json:"message"`
			Status  int    `json:"status"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || len(got.Errors) != 1 ||
		got.Errors[0].Status != http.StatusServiceUnavailable ||
		!strings.HasPrefix(got.Errors[0].Message, item.ErrCollected.Error()) {
		t.Fatal(res.Body, err)
	}
	if res.Code != http.StatusServiceUnavailable || res.Header().Get("Retry-After") != "1" {
		t.Fatal(res.Code, res.Header(), res.Body)
	}
	wantStatus(t, w.do(http.MethodPut, "/artifactory/libs-local/b.txt", "admin", body), 201)
}

// TestAccSupersededRemoteFetchHintsARetry: a remote read that a change of
// the remote's configuration superseded answers 503 "Service Unavailable"
// with Retry-After: 1, since a repeat reads under the new configuration.
func TestAccSupersededRemoteFetchHintsARetry(t *testing.T) {
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				return source.Result{}, source.ErrSuperseded
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		res := w.do(method, "/artifactory/up/a.bin", "reader", "")
		want := `{"errors":[{"message":"Service Unavailable","status":503}]}` + "\n"
		if res.Code != http.StatusServiceUnavailable || res.Header().Get("Retry-After") != "1" ||
			method == http.MethodGet && res.Body.String() != want {
			t.Fatal(method, res.Code, res.Header(), res.Body)
		}
	}
}

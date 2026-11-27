package peer_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type downloadReceiver func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error)

func (f downloadReceiver) Downloads(ctx context.Context, p model.Peer, key string, r cleanup.Request) (cleanup.Answer, error) {
	return f(ctx, p, key, r)
}

func TestAccCleanupSignedTransport(t *testing.T) {
	var remoteID string
	var calls int
	var failure error
	observer := downloadReceiver(func(_ context.Context, caller model.Peer, key string, r cleanup.Request) (cleanup.Answer, error) {
		calls++
		if key != "fed" || caller.InstanceID == "" {
			return cleanup.Answer{}, kernel.ErrInvalid
		}
		if failure != nil {
			return cleanup.Answer{}, failure
		}
		out := cleanup.Answer{
			Instance:    remoteID,
			Incarnation: "repository-incarnation",
			Generation:  "configuration-generation",
			Grant:       "remote-grant",
			Challenge:   r.Challenge,
			PackageType: r.PackageType,
		}
		for n, version := range r.Versions {
			out.Versions = append(out.Versions, api.CleanupDownload{CleanupVersion: version, ID: int64(n + 1), Revision: "download-generation", Created: time.Now().UTC()})
		}
		return out, nil
	})
	a, b := newNode(t), newNode(t, observer)
	remoteID = b.id(t).InstanceID
	connect(t, a, b)
	token, err := a.svc.Issue(t.Context(), model.UseFederatedRepo, a.srv.URL+"/artifactory")
	storekit.Must(t, err)
	storekit.Must(t, b.svc.Redeem(t.Context(), model.UseFederatedRepo, token, b.srv.URL+"/artifactory"))
	factory := storekit.Get[*peeradapter.Factory](t)(peeradapter.NewFactory(peeradapter.ClientOptions{Signer: a.signer, AllowedPrivateOrigins: []string{b.srv.URL}, Attempts: 3}))
	remote := peeradapter.CleanupRemote{Factory: factory}
	peer := storekit.Get[model.Peer](t)(a.store.Peers().Get(t.Context(), remoteID))
	request := cleanup.Request{SourceRepository: "source", PackageType: "npm", Challenge: rand.Text()}
	for n := range api.CleanupBatchSize {
		request.Versions = append(request.Versions, api.CleanupVersion{Package: "@scope/name", Version: fmt.Sprint(n)})
	}
	answer := storekit.Get[cleanup.Answer](t)(remote.Downloads(t.Context(), peer, "fed", request))
	if calls != 1 || len(answer.Versions) != api.CleanupBatchSize || answer.Grant != "remote-grant" || answer.Challenge != request.Challenge {
		t.Fatal("signed batch lost its binding or counters", calls, len(answer.Versions))
	}
	failure = kernel.ErrStore
	_, err = remote.Downloads(t.Context(), peer, "fed", request)
	storekit.WantErr(t, err, cleanup.ErrUnavailable)
	if calls != 2 {
		t.Fatal("cleanup observation retried a failed member", calls)
	}
	failure = nil
	wrong, _, err := ed25519.GenerateKey(rand.Reader)
	storekit.Must(t, err)
	peer.PublicKey = wrong
	_, err = remote.Downloads(t.Context(), peer, "fed", request)
	var unverified *peeradapter.UnverifiedAnswerError
	if !errors.As(err, &unverified) {
		t.Fatal("unverified observation became usable evidence", err)
	}
	peer.PublicKey = b.id(t).PublicKey
	large := request
	large.Versions = []api.CleanupVersion{{Package: strings.Repeat("x", (4<<20)+1), Version: "1"}}
	_, err = remote.Downloads(t.Context(), peer, "fed", large)
	storekit.WantErr(t, err, peeradapter.ErrTooLarge)
}

func TestUnitCleanupObservationResponses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", kernel.ErrInvalid, http.StatusBadRequest},
		{"forbidden", kernel.ErrForbidden, http.StatusForbidden},
		{"not_found", kernel.ErrNotFound, http.StatusNotFound},
		{"unavailable", cleanup.ErrUnavailable, http.StatusConflict},
		{"changed", kernel.ErrConflict, http.StatusConflict},
	}
	ctx := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: "member"})
	body := &peergen.ObserveRepositoryDownloadsJSONRequestBody{
		SourceRepository: "source",
		Challenge:        "fresh-unique-challenge",
		Versions:         []peergen.CleanupVersion{{Package: "name", Version: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := peeradapter.RepositoryServer{Cleanup: downloadReceiver(func(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error) {
				return cleanup.Answer{}, tc.err
			})}
			_, err := server.ObserveRepositoryDownloads(ctx, peergen.ObserveRepositoryDownloadsRequestObject{RepoKey: "fed", Body: body})
			var response *apierror.Error
			if !errors.As(err, &response) || response.Status != tc.status {
				t.Fatalf("observation error=%v want=%d", err, tc.status)
			}
		})
	}
	server := peeradapter.RepositoryServer{}
	var response *apierror.Error
	_, err := server.ObserveRepositoryDownloads(t.Context(), peergen.ObserveRepositoryDownloadsRequestObject{Body: body})
	if !errors.As(err, &response) || response.Status != http.StatusUnauthorized {
		t.Fatal("unsigned observation was accepted", err)
	}
	_, err = server.ObserveRepositoryDownloads(ctx, peergen.ObserveRepositoryDownloadsRequestObject{})
	if !errors.As(err, &response) || response.Status != http.StatusBadRequest {
		t.Fatal("missing batch was accepted", err)
	}
	_, err = (peeradapter.CleanupRemote{}).Downloads(t.Context(), model.Peer{}, "fed", cleanup.Request{})
	storekit.WantErr(t, err, cleanup.ErrUnavailable)
}

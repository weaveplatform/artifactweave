package peer_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type receiver struct {
	err  error
	got  string
	from string
}

func (r *receiver) Apply(_ context.Context, caller model.Peer, key string, _ map[string]any) error {
	r.got, r.from = key, caller.InstanceID
	return r.err
}

// PUT /repositories/{repoKey} answers the receiver's outcome as the peer
// API documents it (#1930).
func TestUnitRepositoryServerAnswers(t *testing.T) {
	ctx := corepeer.WithCaller(t.Context(), model.Peer{InstanceID: "b"})
	body := &peergen.PutRepositoryConfigurationJSONRequestBody{Stream: "repo:fed", Configuration: map[string]any{"rclass": "federated"}}
	rcv := &receiver{}
	srv := peeradapter.RepositoryServer{Receiver: rcv}
	resp, err := srv.PutRepositoryConfiguration(ctx, peergen.PutRepositoryConfigurationRequestObject{RepoKey: "fed", Body: body})
	if _, ok := resp.(peergen.PutRepositoryConfiguration204Response); err != nil || !ok || rcv.got != "fed" || rcv.from != "b" {
		t.Fatalf("applied: %T %v %+v", resp, err, rcv)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{&federated.RefusalError{Message: "no"}, http.StatusBadRequest},
		{federated.ErrNotFederatedPeer, http.StatusForbidden},
		{federated.ErrOtherRepository, http.StatusConflict},
	} {
		rcv.err = tc.err
		_, err := srv.PutRepositoryConfiguration(ctx, peergen.PutRepositoryConfigurationRequestObject{RepoKey: "fed", Body: body})
		var e *apierror.Error
		if !errors.As(err, &e) || e.Status != tc.status {
			t.Errorf("%v answered %v, want %d", tc.err, err, tc.status)
		}
	}
	rcv.err = kernel.ErrStore
	if _, err := srv.PutRepositoryConfiguration(ctx, peergen.PutRepositoryConfigurationRequestObject{RepoKey: "fed", Body: body}); !errors.Is(err, kernel.ErrStore) {
		t.Errorf("store failure: %v", err)
	}
	var e *apierror.Error
	if _, err := srv.PutRepositoryConfiguration(ctx, peergen.PutRepositoryConfigurationRequestObject{RepoKey: "fed"}); !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		t.Errorf("no body: %v", err)
	}
	if _, err := srv.PutRepositoryConfiguration(
		t.Context(),
		peergen.PutRepositoryConfigurationRequestObject{RepoKey: "fed", Body: body},
	); !errors.As(err, &e) ||
		e.Status != http.StatusUnauthorized {
		t.Errorf("unsigned: %v", err)
	}
}

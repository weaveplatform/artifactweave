package peer

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	corepeer "github.com/weaveplatform/artifactweave/internal/federation/core/peer"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	peergen "github.com/weaveplatform/artifactweave/internal/platform/gen/peer"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	peerclient "github.com/weaveplatform/artifactweave/pkg/client/peer"
)

// CleanupRemote uses the existing request/answer signatures with one attempt,
// strict body bounds and the collector's deadline. It owns no transport pool.
type CleanupRemote struct{ Factory *Factory }

// Downloads makes one request-bound signed observation attempt.
func (r CleanupRemote) Downloads(ctx context.Context, peer model.Peer, key string, request cleanup.Request) (cleanup.Answer, error) {
	if r.Factory == nil {
		return cleanup.Answer{}, cleanup.ErrUnavailable
	}
	factory := *r.Factory
	factory.o.Attempts = 1
	factory.o.AttemptTimeout = cleanup.AttemptTimeout
	factory.o.SocketTimeout = cleanup.AttemptTimeout
	factory.o.MaxRequestBody = 4 << 20
	factory.o.MaxResponseBody = 4 << 20
	client, err := factory.Client(ctx, peer)
	if err != nil {
		return cleanup.Answer{}, err
	}
	defer client.Close()
	remote, err := client.API()
	if err != nil {
		return cleanup.Answer{}, err
	}
	body := peerclient.CleanupDownloadBatch{SourceRepository: request.SourceRepository, PackageType: request.PackageType, Challenge: request.Challenge}
	for _, version := range request.Versions {
		body.Versions = append(body.Versions, peerclient.CleanupVersion{Package: version.Package, Version: version.Version})
	}
	response, err := remote.ObserveRepositoryDownloadsWithResponse(ctx, key, &peerclient.ObserveRepositoryDownloadsParams{}, body)
	if err != nil {
		return cleanup.Answer{}, fmt.Errorf("signed member download observation: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return cleanup.Answer{}, fmt.Errorf("member download observation status %d: %w", response.StatusCode(), cleanup.ErrUnavailable)
	}
	value := response.JSON200
	answer := cleanup.Answer{
		PackageType: value.PackageType,
		Instance:    value.Instance,
		Incarnation: value.Incarnation,
		Generation:  value.Generation,
		Grant:       value.Grant,
		Challenge:   value.Challenge,
	}
	for _, row := range value.Versions {
		answer.Versions = append(
			answer.Versions,
			api.CleanupDownload{
				CleanupVersion: api.CleanupVersion{Package: row.Package, Version: row.Version},
				ID:             row.Id,
				Revision:       row.Revision,
				Created:        row.Created,
				LastDownloaded: row.LastDownloaded,
				AdmittedAt:     row.AdmittedAt, AdmissionRevision: row.AdmissionRevision,
			},
		)
	}
	return answer, nil
}

// CleanupReceiver supplies metadata only after current trust and reciprocal
// membership have been checked by the federation owner.
type CleanupReceiver interface {
	Downloads(context.Context, model.Peer, string, cleanup.Request) (cleanup.Answer, error)
}

// ObserveRepositoryDownloads answers a signed bounded member observation.
func (s RepositoryServer) ObserveRepositoryDownloads(
	ctx context.Context,
	request peergen.ObserveRepositoryDownloadsRequestObject,
) (peergen.ObserveRepositoryDownloadsResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	if request.Body == nil {
		return nil, apierror.New(http.StatusBadRequest, "The body must carry a version batch")
	}
	body := request.Body
	input := cleanup.Request{SourceRepository: body.SourceRepository, PackageType: body.PackageType, Challenge: body.Challenge}
	for _, version := range body.Versions {
		input.Versions = append(input.Versions, api.CleanupVersion{Package: version.Package, Version: version.Version})
	}
	answer, err := s.Cleanup.Downloads(ctx, caller, request.RepoKey, input)
	switch {
	case err == nil:
		response := peergen.CleanupDownloadObservation{
			PackageType: answer.PackageType,
			Instance:    answer.Instance,
			Incarnation: answer.Incarnation,
			Generation:  answer.Generation,
			Grant:       answer.Grant,
			Challenge:   answer.Challenge,
			Versions:    []peergen.CleanupDownload{},
		}
		for _, row := range answer.Versions {
			response.Versions = append(
				response.Versions,
				peergen.CleanupDownload{
					Package:           row.Package,
					Version:           row.Version,
					Id:                row.ID,
					Revision:          row.Revision,
					Created:           row.Created,
					LastDownloaded:    row.LastDownloaded,
					AdmittedAt:        row.AdmittedAt,
					AdmissionRevision: row.AdmissionRevision,
				},
			)
		}
		return peergen.ObserveRepositoryDownloads200JSONResponse(response), nil
	case errors.Is(err, kernel.ErrInvalid):
		return nil, apierror.New(http.StatusBadRequest, "Invalid bounded version identities")
	case errors.Is(err, kernel.ErrForbidden):
		return nil, apierror.New(http.StatusForbidden, "Current trust or membership does not permit this observation")
	case errors.Is(err, kernel.ErrNotFound):
		return nil, apierror.New(http.StatusNotFound, "The federated repository no longer exists")
	case errors.Is(err, cleanup.ErrUnavailable), errors.Is(err, kernel.ErrConflict):
		return nil, apierror.New(http.StatusConflict, "Current version download evidence is unavailable")
	default:
		return nil, err //nolint:wrapcheck // the strict error handler preserves and logs the owner failure
	}
}

// Coordinate makes one signed, bounded durable participant request.
func (r CleanupRemote) Coordinate(ctx context.Context, peer model.Peer, key string, request cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
	if r.Factory == nil {
		return cleanup.CoordinationAnswer{}, cleanup.ErrUnavailable
	}
	factory := *r.Factory
	factory.o.Attempts = 1
	factory.o.AttemptTimeout = cleanup.AttemptTimeout
	factory.o.SocketTimeout = cleanup.AttemptTimeout
	factory.o.MaxRequestBody = 4 << 20
	factory.o.MaxResponseBody = 4 << 20
	client, err := factory.Client(ctx, peer)
	if err != nil {
		return cleanup.CoordinationAnswer{}, err
	}
	defer client.Close()
	remote, err := client.API()
	if err != nil {
		return cleanup.CoordinationAnswer{}, err
	}
	ref := request.Exclusion
	wire := peerclient.CleanupExclusion{
		Id:          ref.ID,
		Owner:       ref.Owner,
		Repository:  ref.Repository,
		Incarnation: ref.Incarnation,
		Generation:  ref.Generation,
		Versions:    []peerclient.CleanupExclusionVersion{},
	}
	for _, v := range ref.Versions {
		wire.Versions = append(wire.Versions, peerclient.CleanupExclusionVersion{Package: v.Package, Version: v.Version, Id: v.ID})
	}
	body := peerclient.CleanupCoordinationRequest{
		Action:           peerclient.CleanupCoordinationRequestAction(request.Action),
		SourceRepository: request.SourceRepository,
		PackageType:      request.PackageType,
		Exclusion:        wire,
	}
	response, err := remote.CoordinateRepositoryCleanupWithResponse(ctx, key, &peerclient.CoordinateRepositoryCleanupParams{}, body)
	if err != nil {
		return cleanup.CoordinationAnswer{}, fmt.Errorf("signed cleanup coordination: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return cleanup.CoordinationAnswer{}, fmt.Errorf("cleanup coordination status %d: %w", response.StatusCode(), cleanup.ErrUnavailable)
	}
	value := response.JSON200
	exclusion := artifactsapi.CleanupExclusion{
		ID:          value.Exclusion.Id,
		Owner:       value.Exclusion.Owner,
		Repository:  value.Exclusion.Repository,
		Incarnation: value.Exclusion.Incarnation,
		Generation:  value.Exclusion.Generation,
		Versions:    []artifactsapi.CleanupVersionIdentity{},
	}
	for _, v := range value.Exclusion.Versions {
		exclusion.Versions = append(
			exclusion.Versions,
			artifactsapi.CleanupVersionIdentity{DownloadVersion: artifactsapi.DownloadVersion{Package: v.Package, Version: v.Version}, ID: v.Id},
		)
	}
	return cleanup.CoordinationAnswer{State: string(value.State), Watermark: value.Watermark, Exclusion: exclusion}, nil
}

// CoordinateRepositoryCleanup serves signed durable participant coordination.
func (s RepositoryServer) CoordinateRepositoryCleanup(
	ctx context.Context,
	request peergen.CoordinateRepositoryCleanupRequestObject,
) (peergen.CoordinateRepositoryCleanupResponseObject, error) {
	caller, ok := corepeer.Caller(ctx)
	if !ok {
		return nil, apierror.New(http.StatusUnauthorized, notSigned)
	}
	if request.Body == nil {
		return nil, apierror.New(http.StatusBadRequest, "The body must carry an exact cleanup reservation")
	}
	receiver, ok := s.Cleanup.(interface {
		Coordinate(context.Context, model.Peer, string, cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error)
	})
	if !ok {
		return nil, apierror.New(http.StatusConflict, "Durable cleanup coordination is unavailable")
	}
	body := request.Body
	v := body.Exclusion
	ref := artifactsapi.CleanupExclusion{
		ID:          v.Id,
		Owner:       v.Owner,
		Repository:  v.Repository,
		Incarnation: v.Incarnation,
		Generation:  v.Generation,
		Versions:    []artifactsapi.CleanupVersionIdentity{},
	}
	for _, version := range v.Versions {
		ref.Versions = append(
			ref.Versions,
			artifactsapi.CleanupVersionIdentity{DownloadVersion: artifactsapi.DownloadVersion{Package: version.Package, Version: version.Version}, ID: version.Id},
		)
	}
	answer, err := receiver.Coordinate(
		ctx,
		caller,
		request.RepoKey,
		cleanup.CoordinationRequest{Action: string(body.Action), SourceRepository: body.SourceRepository, PackageType: body.PackageType, Exclusion: ref},
	)
	switch {
	case err == nil:
		ref := answer.Exclusion
		wire := peergen.CleanupExclusion{
			Id:          ref.ID,
			Owner:       ref.Owner,
			Repository:  ref.Repository,
			Incarnation: ref.Incarnation,
			Generation:  ref.Generation,
			Versions:    []peergen.CleanupExclusionVersion{},
		}
		for _, version := range ref.Versions {
			wire.Versions = append(wire.Versions, peergen.CleanupExclusionVersion{Package: version.Package, Version: version.Version, Id: version.ID})
		}
		return peergen.CoordinateRepositoryCleanup200JSONResponse{State: peergen.CleanupCoordinationOutcomeState(answer.State), Watermark: answer.Watermark, Exclusion: wire}, nil
	case errors.Is(err, kernel.ErrInvalid):
		return nil, apierror.New(http.StatusBadRequest, "Invalid bounded cleanup reservation")
	case errors.Is(err, kernel.ErrForbidden):
		return nil, apierror.New(http.StatusForbidden, "Current trust or membership does not permit coordination")
	case errors.Is(err, kernel.ErrNotFound):
		return nil, apierror.New(http.StatusNotFound, "The exact cleanup operation or repository is unavailable")
	case errors.Is(err, kernel.ErrConflict), errors.Is(err, cleanup.ErrUnavailable), errors.Is(err, artifactsapi.ErrCleanupExcluded):
		return nil, apierror.New(http.StatusConflict, "Cleanup exclusion or its terminal applied outcome is unavailable")
	default:
		return nil, fmt.Errorf("serve cleanup coordination: %w", err)
	}
}

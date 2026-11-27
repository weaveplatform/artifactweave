package federationhttp

import (
	"context"
	"net/http"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const migrationToolOnly = "Access denied: This endpoint is restricted to migration tool calls only."

// GetFederationInternalStatus serves research 40 section 7's answer for GET federation/status.
func (a FederatedAPI) GetFederationInternalStatus(
	ctx context.Context,
	_ artgen.GetFederationInternalStatusRequestObject,
) (artgen.GetFederationInternalStatusResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// ValidateFederationRepository serves research 40 section 7's answer for GET federation/validate/{repoKey}.
func (a FederatedAPI) ValidateFederationRepository(
	ctx context.Context,
	_ artgen.ValidateFederationRepositoryRequestObject,
) (artgen.ValidateFederationRepositoryResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationInternalRepositories serves research 40 section 7's answer for GET federation/repos.
func (a FederatedAPI) GetFederationInternalRepositories(
	ctx context.Context,
	_ artgen.GetFederationInternalRepositoriesRequestObject,
) (artgen.GetFederationInternalRepositoriesResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationBaseUrlAndRepositories serves research 40 section 7's answer for GET federation/baseUrlAndRepos.
func (a FederatedAPI) GetFederationBaseUrlAndRepositories(
	ctx context.Context,
	_ artgen.GetFederationBaseUrlAndRepositoriesRequestObject,
) (artgen.GetFederationBaseUrlAndRepositoriesResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationInternalProperties serves research 40 section 7's answer for POST federation/properties/{repoKey}.
func (a FederatedAPI) GetFederationInternalProperties(
	ctx context.Context,
	_ artgen.GetFederationInternalPropertiesRequestObject,
) (artgen.GetFederationInternalPropertiesResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationInternalProperty serves research 40 section 7's answer for GET federation/property/{repoKey}/{fileName}.
func (a FederatedAPI) GetFederationInternalProperty(
	ctx context.Context,
	_ artgen.GetFederationInternalPropertyRequestObject,
) (artgen.GetFederationInternalPropertyResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationExtendedInfo serves research 40 section 7's answer for GET federation/extendedInfo/{repoKey}.
func (a FederatedAPI) GetFederationExtendedInfo(ctx context.Context, _ artgen.GetFederationExtendedInfoRequestObject) (artgen.GetFederationExtendedInfoResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationRepositoryReady serves research 40 section 7's answer for GET federation/ready/{repoKey}.
func (a FederatedAPI) GetFederationRepositoryReady(
	ctx context.Context,
	_ artgen.GetFederationRepositoryReadyRequestObject,
) (artgen.GetFederationRepositoryReadyResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationRepositoryExists serves research 40 section 7's answer for GET federation/repoExists/{repoKey}.
func (a FederatedAPI) GetFederationRepositoryExists(
	ctx context.Context,
	_ artgen.GetFederationRepositoryExistsRequestObject,
) (artgen.GetFederationRepositoryExistsResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PostFederationInternalMetadata serves research 40 section 7's answer for POST federation/metadata.
func (a FederatedAPI) PostFederationInternalMetadata(
	ctx context.Context,
	_ artgen.PostFederationInternalMetadataRequestObject,
) (artgen.PostFederationInternalMetadataResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PutFederationInternalMetadata serves research 40 section 7's answer for PUT federation/metadata.
func (a FederatedAPI) PutFederationInternalMetadata(
	ctx context.Context,
	_ artgen.PutFederationInternalMetadataRequestObject,
) (artgen.PutFederationInternalMetadataResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// InvalidateFederationInternalMetadata serves research 40 section 7's answer for POST federation/metadata/invalidate.
func (a FederatedAPI) InvalidateFederationInternalMetadata(
	ctx context.Context,
	_ artgen.InvalidateFederationInternalMetadataRequestObject,
) (artgen.InvalidateFederationInternalMetadataResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// DisableFederationNegotiation serves research 40 section 7's answer for PUT federation/negotiation/disable.
func (a FederatedAPI) DisableFederationNegotiation(
	ctx context.Context,
	_ artgen.DisableFederationNegotiationRequestObject,
) (artgen.DisableFederationNegotiationResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// EnableFederationInternalRepositories serves research 40 section 7's answer for POST federation/repos/enable.
func (a FederatedAPI) EnableFederationInternalRepositories(
	ctx context.Context,
	_ artgen.EnableFederationInternalRepositoriesRequestObject,
) (artgen.EnableFederationInternalRepositoriesResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PostFederationDryFullSyncFile serves research 40 section 7's answer for POST federation/dryfullsync/repo/{fileName}.
func (a FederatedAPI) PostFederationDryFullSyncFile(
	ctx context.Context,
	_ artgen.PostFederationDryFullSyncFileRequestObject,
) (artgen.PostFederationDryFullSyncFileResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PostFederationFullSyncFile serves research 40 section 7's answer for POST federation/fullsync/repo/{fileName}.
func (a FederatedAPI) PostFederationFullSyncFile(ctx context.Context, _ artgen.PostFederationFullSyncFileRequestObject) (artgen.PostFederationFullSyncFileResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationFullSyncFile serves research 40 section 7's answer for GET federation/fullsync/repo/{fileName}.
func (a FederatedAPI) GetFederationFullSyncFile(ctx context.Context, _ artgen.GetFederationFullSyncFileRequestObject) (artgen.GetFederationFullSyncFileResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// DeleteFederationFullSyncFile serves research 40 section 7's answer for DELETE federation/fullsync/repo/{fileName}.
func (a FederatedAPI) DeleteFederationFullSyncFile(
	ctx context.Context,
	_ artgen.DeleteFederationFullSyncFileRequestObject,
) (artgen.DeleteFederationFullSyncFileResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PostFederationFullSyncSystemRepository serves research 40 section 7's answer for POST federation/fullsync/systemRepo.
func (a FederatedAPI) PostFederationFullSyncSystemRepository(
	ctx context.Context,
	_ artgen.PostFederationFullSyncSystemRepositoryRequestObject,
) (artgen.PostFederationFullSyncSystemRepositoryResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationRepositoryConfigInfo serves research 40 section 7's answer for GET federation/federatedRepoConfigInfo.
func (a FederatedAPI) GetFederationRepositoryConfigInfo(
	ctx context.Context,
	_ artgen.GetFederationRepositoryConfigInfoRequestObject,
) (artgen.GetFederationRepositoryConfigInfoResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationConfigInfo serves research 40 section 7's answer for GET federation/federatedConfigInfo/{repoKey}.
func (a FederatedAPI) GetFederationConfigInfo(ctx context.Context, _ artgen.GetFederationConfigInfoRequestObject) (artgen.GetFederationConfigInfoResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// DisableFederationRemoteMember serves research 40 section 7's answer for POST federation/disableRemoteMember.
func (a FederatedAPI) DisableFederationRemoteMember(
	ctx context.Context,
	_ artgen.DisableFederationRemoteMemberRequestObject,
) (artgen.DisableFederationRemoteMemberResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// EnableFederationInternalMember serves research 40 section 7's answer for POST federation/member/enable.
func (a FederatedAPI) EnableFederationInternalMember(
	ctx context.Context,
	_ artgen.EnableFederationInternalMemberRequestObject,
) (artgen.EnableFederationInternalMemberResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationLastIdInSystem serves research 40 section 7's answer for GET federation/lastIdInSystem.
func (a FederatedAPI) GetFederationLastIdInSystem(
	ctx context.Context,
	_ artgen.GetFederationLastIdInSystemRequestObject,
) (artgen.GetFederationLastIdInSystemResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// SubmitFederationInternalTask serves research 40 section 7's answer for POST federation/task/submit.
func (a FederatedAPI) SubmitFederationInternalTask(
	ctx context.Context,
	_ artgen.SubmitFederationInternalTaskRequestObject,
) (artgen.SubmitFederationInternalTaskResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// CheckFederationInternalBinary serves research 40 section 7's answer for GET federation/binary/provider/v2/check.
func (a FederatedAPI) CheckFederationInternalBinary(
	ctx context.Context,
	_ artgen.CheckFederationInternalBinaryRequestObject,
) (artgen.CheckFederationInternalBinaryResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationQueueRemaining serves research 40 section 7's answer for POST federation/queue/remaining.
func (a FederatedAPI) GetFederationQueueRemaining(
	ctx context.Context,
	_ artgen.GetFederationQueueRemainingRequestObject,
) (artgen.GetFederationQueueRemainingResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// StopFederationQueue serves research 40 section 7's answer for PUT federation/queue/stop.
func (a FederatedAPI) StopFederationQueue(ctx context.Context, _ artgen.StopFederationQueueRequestObject) (artgen.StopFederationQueueResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// DeregisterFederationQueue serves research 40 section 7's answer for PUT federation/queue/deregister.
func (a FederatedAPI) DeregisterFederationQueue(ctx context.Context, _ artgen.DeregisterFederationQueueRequestObject) (artgen.DeregisterFederationQueueResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// ResetFederationQueue serves research 40 section 7's answer for POST federation/queue/reset.
func (a FederatedAPI) ResetFederationQueue(ctx context.Context, _ artgen.ResetFederationQueueRequestObject) (artgen.ResetFederationQueueResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationQueueState serves research 40 section 7's answer for GET federation/queue/state.
func (a FederatedAPI) GetFederationQueueState(ctx context.Context, _ artgen.GetFederationQueueStateRequestObject) (artgen.GetFederationQueueStateResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PutFederationQueueState serves research 40 section 7's answer for PUT federation/queue/state/{state}.
func (a FederatedAPI) PutFederationQueueState(ctx context.Context, _ artgen.PutFederationQueueStateRequestObject) (artgen.PutFederationQueueStateResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationTenantMoveStatus serves research 40 section 7's answer for GET federation/tenant/move/status.
func (a FederatedAPI) GetFederationTenantMoveStatus(
	ctx context.Context,
	_ artgen.GetFederationTenantMoveStatusRequestObject,
) (artgen.GetFederationTenantMoveStatusResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// SnapshotFederationRepositories serves research 40 section 7's answer for PUT federation/snapshotFederatedRepos.
func (a FederatedAPI) SnapshotFederationRepositories(
	ctx context.Context,
	_ artgen.SnapshotFederationRepositoriesRequestObject,
) (artgen.SnapshotFederationRepositoriesResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationVerboseMigrationStatus serves research 40 section 7's answer for GET federation/migration/status/verbose.
func (a FederatedAPI) GetFederationVerboseMigrationStatus(
	ctx context.Context,
	_ artgen.GetFederationVerboseMigrationStatusRequestObject,
) (artgen.GetFederationVerboseMigrationStatusResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PutFederationInternalBinaryTasks serves research 40 section 7's answer for PUT federation/binaries/rtfs.
func (a FederatedAPI) PutFederationInternalBinaryTasks(
	ctx context.Context,
	_ artgen.PutFederationInternalBinaryTasksRequestObject,
) (artgen.PutFederationInternalBinaryTasksResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// GetFederationInternalBinaryTasks serves research 40 section 7's answer for GET federation/binaries/rtfs.
func (a FederatedAPI) GetFederationInternalBinaryTasks(
	ctx context.Context,
	_ artgen.GetFederationInternalBinaryTasksRequestObject,
) (artgen.GetFederationInternalBinaryTasksResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PostFederationGridTopologyEvents serves research 40 section 7's answer for POST federation/grid/topology/events.
func (a FederatedAPI) PostFederationGridTopologyEvents(
	ctx context.Context,
	_ artgen.PostFederationGridTopologyEventsRequestObject,
) (artgen.PostFederationGridTopologyEventsResponseObject, error) {
	return nil, apierror.New(http.StatusNotFound, "Not Found")
}

// PutFederationServiceConnection serves research 40 section 7's answer for PUT federation/rtfs.
func (a FederatedAPI) PutFederationServiceConnection(
	ctx context.Context,
	_ artgen.PutFederationServiceConnectionRequestObject,
) (artgen.PutFederationServiceConnectionResponseObject, error) {
	return nil, apierror.New(http.StatusBadRequest, migrationToolOnly)
}

// PutFederationMigration serves research 40 section 7's answer for PUT federation/migration.
func (a FederatedAPI) PutFederationMigration(ctx context.Context, _ artgen.PutFederationMigrationRequestObject) (artgen.PutFederationMigrationResponseObject, error) {
	return nil, apierror.New(http.StatusBadRequest, migrationToolOnly)
}

// PutFederationVerboseMigrationStatus serves research 40 section 7's answer for PUT federation/migration/status/verbose.
func (a FederatedAPI) PutFederationVerboseMigrationStatus(
	ctx context.Context,
	_ artgen.PutFederationVerboseMigrationStatusRequestObject,
) (artgen.PutFederationVerboseMigrationStatusResponseObject, error) {
	return nil, apierror.New(http.StatusBadRequest, migrationToolOnly)
}

// GetFederationServiceConnection serves research 40 section 7's answer for GET federation/rtfs.
func (a FederatedAPI) GetFederationServiceConnection(
	ctx context.Context,
	_ artgen.GetFederationServiceConnectionRequestObject,
) (artgen.GetFederationServiceConnectionResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.Administrator(ctx); err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.GetFederationServiceConnection200TextResponse("true"), nil
}

// GetFederationMigrationStatus serves research 40 section 7's answer for GET federation/migration/status.
func (a FederatedAPI) GetFederationMigrationStatus(
	ctx context.Context,
	_ artgen.GetFederationMigrationStatusRequestObject,
) (artgen.GetFederationMigrationStatusResponseObject, error) {
	ops, err := a.ops()
	if err != nil {
		return nil, err
	}
	if err := ops.Administrator(ctx); err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.GetFederationMigrationStatus200TextResponse("MIGRATION_COMPLETED"), nil
}

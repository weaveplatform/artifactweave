package federationhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// FederationMonitoring is the read model shared by the status operations.
type FederationMonitoring interface {
	Read(context.Context, string, bool) (federated.RepositoryStatus, error)
	List(context.Context) ([]federated.RepositoryStatus, error)
	ListWithBase(context.Context) ([]federated.RepositoryStatus, error)
	Inconsistent(context.Context) ([]string, error)
}

func (a FederatedAPI) monitor() (FederationMonitoring, error) {
	if a.Monitoring == nil {
		return nil, apierror.ErrNotImplemented
	}
	return a.Monitoring, nil
}

func (a FederatedAPI) repositoryStatus(ctx context.Context, key, projection string, v2 bool) (artgen.FederatedRepositoryStatus, error) {
	query, err := a.monitor()
	if err != nil {
		return artgen.FederatedRepositoryStatus{}, err
	}
	view, err := query.Read(ctx, key, true)
	if errors.Is(err, federated.ErrNoRepository) {
		return artgen.FederatedRepositoryStatus{}, apierror.New(http.StatusNotFound, fmt.Sprintf("Repo key: %s not found", key))
	}
	if err != nil {
		return artgen.FederatedRepositoryStatus{}, federatedError(ctx, key, err)
	}
	if projection != "" && projection != "mirrors" && projection != "binaries" {
		return artgen.FederatedRepositoryStatus{}, apierror.New(http.StatusBadRequest, "Could not get repo status invalid url")
	}
	body := repositoryStatusWire(view, v2)
	switch projection {
	case "mirrors":
		body.BinariesTasksInfo, body.FederatedArtifactStatus = nil, nil
	case "binaries":
		body.MirrorEventsStatusInfo = nil
	}
	return body, nil
}

func repositoryStatusWire(view federated.RepositoryStatus, v2 bool) artgen.FederatedRepositoryStatus {
	mirrors := make([]artgen.FederatedMirrorEvents, 0, len(view.Members))
	for _, row := range view.Members {
		status := "OUT_OF_SYNC"
		if row.Status == "HEALTHY" {
			status = "SYNCED"
		}
		if v2 {
			status = mirrorStatusV2(row)
		}
		mirrors = append(mirrors, artgen.FederatedMirrorEvents{
			RemoteUrl: &row.Member.BaseURL, RemoteRepoKey: &row.Member.RemoteRepoKey, Status: &status,
			CreateEvents: &row.CreateEvents, UpdateEvents: &row.UpdateEvents, DeleteEvents: &row.DeleteEvents, PropsEvents: &row.PropsEvents,
			ErrorEvents: &row.ErrorEvents, LagInMS: &row.LagInMS, EventRegistrationTimeStamp: &row.RegisteredAt,
		})
	}
	return artgen.FederatedRepositoryStatus{
		LocalKey:               &view.Key,
		MirrorEventsStatusInfo: &mirrors,
		BinariesTasksInfo: &artgen.FederatedStatusBinaryTasks{
			FailingTasks:    httpkit.PtrTo(int64(view.FailedTasks)),
			InProgressTasks: httpkit.PtrTo(int64(view.InProgressTasks)),
		},
		FederatedArtifactStatus: &artgen.FederatedArtifactStatus{CountFullyReplicateArtifacts: &view.FullyReplicated, CountArtificiallyReplicatedArtifacts: &view.MetadataOnly},
	}
}

func mirrorStatusV2(row federated.MemberStatus) string {
	switch row.Status {
	case "HEALTHY":
		if row.Mode == "Receiving_Only" {
			return "READ_ONLY"
		}
		return "HEALTHY"
	case "DELAYED":
		return "ERROR_OUT_OF_SYNC"
	case "DISABLED", "N/A":
		return "ERROR_INACTIVE"
	case "ERROR", "UNSUPPORTED":
		return "ERROR_GENERIC"
	default:
		return row.Status
	}
}

// GetFederatedRepositoryStatus serves the legacy repository status DTO.
func (a FederatedAPI) GetFederatedRepositoryStatus(
	ctx context.Context,
	req artgen.GetFederatedRepositoryStatusRequestObject,
) (artgen.GetFederatedRepositoryStatusResponseObject, error) {
	body, err := a.repositoryStatus(ctx, req.RepoKey, "", false)
	if err != nil {
		return nil, err
	}
	return artgen.GetFederatedRepositoryStatus200JSONResponse(body), nil
}

// GetFederatedRepositoryStatusV2 serves the same DTO with detailed mirror states.
func (a FederatedAPI) GetFederatedRepositoryStatusV2(
	ctx context.Context,
	req artgen.GetFederatedRepositoryStatusV2RequestObject,
) (artgen.GetFederatedRepositoryStatusV2ResponseObject, error) {
	body, err := a.repositoryStatus(ctx, req.RepoKey, "", true)
	if err != nil {
		return nil, err
	}
	return artgen.GetFederatedRepositoryStatusV2200JSONResponse(body), nil
}

// GetFederatedRepositoryStatusProjection selects mirrors or binaries, as the
// 7.161.26 resource does; the last path segment is not a member identifier.
func (a FederatedAPI) GetFederatedRepositoryStatusProjection(
	ctx context.Context,
	req artgen.GetFederatedRepositoryStatusProjectionRequestObject,
) (artgen.GetFederatedRepositoryStatusProjectionResponseObject, error) {
	body, err := a.repositoryStatus(ctx, req.RepoKey, req.FederatedKey, false)
	if err != nil {
		return nil, err
	}
	return artgen.GetFederatedRepositoryStatusProjection200JSONResponse(body), nil
}

// GetFederatedRepositoryStatusProjectionV2 selects the v2 mirrors or binaries DTO.
func (a FederatedAPI) GetFederatedRepositoryStatusProjectionV2(
	ctx context.Context,
	req artgen.GetFederatedRepositoryStatusProjectionV2RequestObject,
) (artgen.GetFederatedRepositoryStatusProjectionV2ResponseObject, error) {
	body, err := a.repositoryStatus(ctx, req.RepoKey, req.FederatedKey, true)
	if err != nil {
		return nil, err
	}
	return artgen.GetFederatedRepositoryStatusProjectionV2200JSONResponse(body), nil
}

type syncMemberWire = struct {
	RemoteKey *string `json:"remoteKey,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	RemoteUrl *string `json:"remoteUrl,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
}
type syncStateWire = struct {
	Key     *string                                                 `json:"key,omitempty"`     //nolint:tagliatelle // matches the generated JFrog response
	Members *[]syncMemberWire                                       `json:"members,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	Status  *artgen.GetFederationSyncState200JSONResponseBodyStatus `json:"status,omitempty"`  //nolint:tagliatelle // matches the generated JFrog response
}

// GetFederationSyncState lists repositories using DeriveSyncStatus's states.
func (a FederatedAPI) GetFederationSyncState(ctx context.Context, _ artgen.GetFederationSyncStateRequestObject) (artgen.GetFederationSyncStateResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.List(ctx)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	result := make(artgen.GetFederationSyncState200JSONResponse, 0, len(rows))
	for _, row := range rows {
		members := make([]syncMemberWire, 0, len(row.Members))
		for _, member := range row.Members {
			members = append(members, syncMemberWire{RemoteKey: &member.Member.RemoteRepoKey, RemoteUrl: &member.Member.BaseURL})
		}
		result = append(result, syncStateWire{Key: &row.Key, Members: &members, Status: httpkit.PtrTo(artgen.GetFederationSyncState200JSONResponseBodyStatus(row.Status))})
	}
	return result, nil
}

type lagWire = struct {
	EventRegistrationTimeStamp *int64  `json:"eventRegistrationTimeStamp,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	LagInMS                    *int64  `json:"lagInMS,omitempty"`                    //nolint:tagliatelle // matches the generated JFrog response
	LocalRepoKey               *string `json:"localRepoKey,omitempty"`               //nolint:tagliatelle // matches the generated JFrog response
	RemoteRepoKey              *string `json:"remoteRepoKey,omitempty"`              //nolint:tagliatelle // matches the generated JFrog response
	RemoteUrl                  *string `json:"remoteUrl,omitempty"`                  //nolint:tagliatelle // matches the generated JFrog response
}

// GetFederationMirrorLagTime lists members past the requested lag threshold.
func (a FederatedAPI) GetFederationMirrorLagTime(ctx context.Context, req artgen.GetFederationMirrorLagTimeRequestObject) (artgen.GetFederationMirrorLagTimeResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.List(ctx)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	threshold := deref(req.Params.ThresholdTimeInMs)
	result := make(artgen.GetFederationMirrorLagTime200JSONResponse, 0)
	for _, row := range rows {
		for _, member := range row.Members {
			if member.LagInMS <= threshold {
				continue
			}
			result = append(
				result,
				lagWire{
					LocalRepoKey:               &row.Key,
					RemoteRepoKey:              &member.Member.RemoteRepoKey,
					RemoteUrl:                  &member.Member.BaseURL,
					LagInMS:                    &member.LagInMS,
					EventRegistrationTimeStamp: &member.RegisteredAt,
				},
			)
		}
	}
	return result, nil
}

type unavailableWire = struct {
	LocalRepoKey  *string `json:"localRepoKey,omitempty"`  //nolint:tagliatelle // matches the generated JFrog response
	RemoteRepoKey *string `json:"remoteRepoKey,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	RemoteUrl     *string `json:"remoteUrl,omitempty"`     //nolint:tagliatelle // matches the generated JFrog response
	Status        *string `json:"status,omitempty"`        //nolint:tagliatelle // matches the generated JFrog response
}

// GetUnavailableMirrors reports unavailable and not-yet-synchronizing members.
func (a FederatedAPI) GetUnavailableMirrors(ctx context.Context, _ artgen.GetUnavailableMirrorsRequestObject) (artgen.GetUnavailableMirrorsResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.List(ctx)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	result := make(artgen.GetUnavailableMirrors200JSONResponse, 0)
	for _, row := range rows {
		for _, member := range row.Members {
			if member.Status == "HEALTHY" || member.Status == "DELAYED" {
				continue
			}
			result = append(result, unavailableWire{LocalRepoKey: &row.Key, RemoteRepoKey: &member.Member.RemoteRepoKey, RemoteUrl: &member.Member.BaseURL, Status: &member.Status})
		}
	}
	return result, nil
}

type memberInfoWire = struct {
	AggregatedStatus *string `json:"aggregatedStatus,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	IsDisabled       *bool   `json:"isDisabled,omitempty"`       //nolint:tagliatelle // matches the generated JFrog response
	IsInLag          *bool   `json:"isInLag,omitempty"`          //nolint:tagliatelle // matches the generated JFrog response
	IsSupported      *bool   `json:"isSupported,omitempty"`      //nolint:tagliatelle // matches the generated JFrog response
	RepoKey          *string `json:"repoKey,omitempty"`          //nolint:tagliatelle // matches the generated JFrog response
	Url              *string `json:"url,omitempty"`              //nolint:tagliatelle // matches the generated JFrog response
}
type federationInfoWire = struct {
	LocalRepoKey   *string                                        `json:"localRepoKey,omitempty"`   //nolint:tagliatelle // matches the generated JFrog response
	MemberInfoList *[]memberInfoWire                              `json:"memberInfoList,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	Priority       *artgen.FederationStateListFederationsPriority `json:"priority,omitempty"`       //nolint:tagliatelle // matches the generated JFrog response
}

func aggregatedStatus(status string) string {
	if status == "N/A" || status == "UNSUPPORTED" {
		return "NOT_AVAILABLE"
	}
	return status
}

// GetFederationStateDetails lists the common read model, optionally by state.
func (a FederatedAPI) GetFederationStateDetails(ctx context.Context, req artgen.GetFederationStateDetailsRequestObject) (artgen.GetFederationStateDetailsResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.ListWithBase(ctx)
	if errors.Is(err, federated.ErrNoConnectionBaseURL) {
		var response artgen.GetFederationStateDetails400JSONResponse
		if err := response.FromFederationStateList(artgen.FederationStateList{Federations: httpkit.PtrTo([]federationInfoWire{})}); err != nil {
			return nil, fmt.Errorf("encode empty federation state list: %w", err)
		}
		return response, nil
	}
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	filters := map[string]bool{}
	if raw := deref(req.Params.Status); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			switch value {
			case "HEALTHY", "READ_ONLY", "NOT_AVAILABLE", "DELAYED", "PENDING_FS", "FULL_SYNC_RUNNING", "ERROR", "DISABLED":
				filters[value] = true
			default:
				return nil, apierror.New(http.StatusBadRequest, "Unknown federation status: "+value)
			}
		}
	}
	result := make([]federationInfoWire, 0, len(rows))
	for _, row := range rows {
		members := make([]memberInfoWire, 0, len(row.Members))
		for _, member := range row.Members {
			state := aggregatedStatus(member.Status)
			if len(filters) > 0 && !filters[state] {
				continue
			}
			members = append(
				members,
				memberInfoWire{
					AggregatedStatus: &state,
					IsDisabled:       &member.Disabled,
					IsInLag:          httpkit.PtrTo(member.LagInMS > federated.StatusLagThreshold.Milliseconds()),
					IsSupported:      &member.Supported,
					RepoKey:          &member.Member.RemoteRepoKey,
					Url:              &member.Member.BaseURL,
				},
			)
		}
		if len(filters) > 0 && len(members) == 0 {
			continue
		}
		result = append(
			result,
			federationInfoWire{
				LocalRepoKey:   &row.Key,
				MemberInfoList: &members,
				Priority:       httpkit.PtrTo(artgen.FederationStateListFederationsPriority(row.Priority)),
			},
		)
	}
	return artgen.GetFederationStateDetails200JSONResponse{Federations: &result}, nil
}

type stateCountsWire = struct {
	DELAYED         *int64 `json:"DELAYED,omitempty"`           //nolint:tagliatelle // matches the generated JFrog response
	DISABLED        *int64 `json:"DISABLED,omitempty"`          //nolint:tagliatelle // matches the generated JFrog response
	ERROR           *int64 `json:"ERROR,omitempty"`             //nolint:tagliatelle // matches the generated JFrog response
	FULLSYNCRUNNING *int64 `json:"FULL_SYNC_RUNNING,omitempty"` //nolint:tagliatelle // matches the generated JFrog response
	HEALTHY         *int64 `json:"HEALTHY,omitempty"`           //nolint:tagliatelle // matches the generated JFrog response
	NOTAVAILABLE    *int64 `json:"NOT_AVAILABLE,omitempty"`     //nolint:tagliatelle // matches the generated JFrog response
	PENDINGFS       *int64 `json:"PENDING_FS,omitempty"`        //nolint:tagliatelle // matches the generated JFrog response
	READONLY        *int64 `json:"READ_ONLY,omitempty"`         //nolint:tagliatelle // matches the generated JFrog response
}

// GetFederationStateSummaries counts the same member states stateList reports.
func (a FederatedAPI) GetFederationStateSummaries(
	ctx context.Context,
	_ artgen.GetFederationStateSummariesRequestObject,
) (artgen.GetFederationStateSummariesResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.ListWithBase(ctx)
	if errors.Is(err, federated.ErrNoConnectionBaseURL) {
		var response artgen.GetFederationStateSummaries400JSONResponse
		if err := response.FromFederationStateSummary(stateSummaryWire(nil)); err != nil {
			return nil, fmt.Errorf("encode empty federation state summary: %w", err)
		}
		return response, nil
	}
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.GetFederationStateSummaries200JSONResponse(stateSummaryWire(rows)), nil
}

func stateSummaryWire(rows []federated.RepositoryStatus) artgen.FederationStateSummary {
	counts := map[string]int64{}
	for _, row := range rows {
		for _, member := range row.Members {
			counts[aggregatedStatus(member.Status)]++
		}
	}
	return artgen.FederationStateSummary{FederationStates: &stateCountsWire{
		HEALTHY:         httpkit.PtrTo(counts["HEALTHY"]),
		NOTAVAILABLE:    httpkit.PtrTo(counts["NOT_AVAILABLE"]),
		DELAYED:         httpkit.PtrTo(counts["DELAYED"]),
		PENDINGFS:       httpkit.PtrTo(counts["PENDING_FS"]),
		FULLSYNCRUNNING: httpkit.PtrTo(counts["FULL_SYNC_RUNNING"]),
		ERROR:           httpkit.PtrTo(counts["ERROR"]),
		DISABLED:        httpkit.PtrTo(counts["DISABLED"]),
		// The source exposes this counter but not its classifier. Receiving-only
		// direction does not create a read-only health state in this model.
		READONLY: httpkit.PtrTo(counts["READ_ONLY"]),
	}}
}

// GetListOfInconsistentFederatedRepositories compares reachable member configurations.
func (a FederatedAPI) GetListOfInconsistentFederatedRepositories(
	ctx context.Context,
	_ artgen.GetListOfInconsistentFederatedRepositoriesRequestObject,
) (artgen.GetListOfInconsistentFederatedRepositoriesResponseObject, error) {
	query, err := a.monitor()
	if err != nil {
		return nil, err
	}
	rows, err := query.Inconsistent(ctx)
	if err != nil {
		return nil, federatedError(ctx, "", err)
	}
	return artgen.GetListOfInconsistentFederatedRepositories200JSONResponse(rows), nil
}

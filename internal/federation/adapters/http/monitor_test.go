package federationhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	federationhttp "github.com/weaveplatform/artifactweave/internal/federation/adapters/http"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

type monitoringService struct {
	view     federated.RepositoryStatus
	err      error
	detailed bool
}

func (m *monitoringService) Read(_ context.Context, _ string, detailed bool) (federated.RepositoryStatus, error) {
	m.detailed = detailed
	return m.view, m.err
}

func (m *monitoringService) List(context.Context) ([]federated.RepositoryStatus, error) {
	return []federated.RepositoryStatus{m.view}, m.err
}

func (m *monitoringService) ListWithBase(ctx context.Context) ([]federated.RepositoryStatus, error) {
	return m.List(ctx)
}

func (m *monitoringService) Inconsistent(context.Context) ([]string, error) {
	return []string{"fed"}, m.err
}

func TestUnitMonitoringAdapterPreservesDTOsAndProjectionCounts(t *testing.T) {
	service := &monitoringService{
		view: federated.RepositoryStatus{
			Key:             "fed",
			FullyReplicated: 4,
			MetadataOnly:    2,
			FailedTasks:     1,
			InProgressTasks: 1,
			Members: []federated.MemberStatus{
				{
					Member:       model.FederationMember{BaseURL: "https://peer/artifactory", RemoteRepoKey: "other"},
					Status:       "HEALTHY",
					CreateEvents: 105,
					UpdateEvents: 3,
					DeleteEvents: 2,
					PropsEvents:  1,
					RegisteredAt: 100,
					LagInMS:      200,
				},
			},
		},
	}
	api := federationhttp.FederatedAPI{Monitoring: service}
	full, err := api.GetFederatedRepositoryStatus(t.Context(), artgen.GetFederatedRepositoryStatusRequestObject{RepoKey: "fed"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(full)
	for _, field := range []string{`"localKey":"fed"`, `"propsEvents":1`, `"createEvents":105`, `"countFullyReplicateArtifacts":4`, `"countArtificiallyReplicatedArtifacts":2`, `"status":"SYNCED"`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("missing %s in %s", field, data)
		}
	}
	for _, selector := range []string{"mirrors", "binaries"} {
		response, err := api.GetFederatedRepositoryStatusProjection(t.Context(), artgen.GetFederatedRepositoryStatusProjectionRequestObject{RepoKey: "fed", FederatedKey: selector})
		if err != nil || !service.detailed {
			t.Fatalf("projection omitted detailed counts: %v", err)
		}
		data, _ := json.Marshal(response)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if selector == "mirrors" {
			if len(fields) != 2 || !strings.Contains(string(fields["mirrorEventsStatusInfo"]), `"createEvents":105`) {
				t.Fatalf("mirrors %s", data)
			}
		} else if len(fields) != 3 || fields["mirrorEventsStatusInfo"] != nil {
			t.Fatalf("binaries %s", data)
		}
	}
	for _, tc := range []struct{ state, mode, want string }{
		{"HEALTHY", "", "HEALTHY"},
		{"HEALTHY", "Receiving_Only", "READ_ONLY"},
		{"DELAYED", "", "ERROR_OUT_OF_SYNC"},
		{"DISABLED", "", "ERROR_INACTIVE"},
		{"N/A", "", "ERROR_INACTIVE"},
		{"ERROR", "", "ERROR_GENERIC"},
		{"UNSUPPORTED", "", "ERROR_GENERIC"},
		{"PENDING_FS", "", "PENDING_FS"},
		{"FULL_SYNC_RUNNING", "", "FULL_SYNC_RUNNING"},
	} {
		service.view.Members[0].Status, service.view.Members[0].Mode = tc.state, tc.mode
		response, err := api.GetFederatedRepositoryStatusV2(t.Context(), artgen.GetFederatedRepositoryStatusV2RequestObject{RepoKey: "fed"})
		if err != nil {
			t.Fatal(err)
		}
		wire := artgen.FederatedRepositoryStatus(response.(artgen.GetFederatedRepositoryStatusV2200JSONResponse))
		if got := *(*wire.MirrorEventsStatusInfo)[0].Status; got != tc.want {
			t.Fatalf("v2 %s/%s -> %s, want %s", tc.state, tc.mode, got, tc.want)
		}
	}
}

func TestUnitMonitoringAdapterFiltersAndCountsCommonMemberStates(t *testing.T) {
	service := &monitoringService{view: federated.RepositoryStatus{Key: "fed", Status: model.SyncOutOfSync, Priority: model.PriorityHigh}}
	for _, state := range []string{"HEALTHY", "DELAYED", "ERROR", "DISABLED", "PENDING_FS", "FULL_SYNC_RUNNING", "UNSUPPORTED", "N/A"} {
		service.view.Members = append(
			service.view.Members,
			federated.MemberStatus{
				Status:       state,
				Supported:    state != "UNSUPPORTED",
				Disabled:     state == "DISABLED",
				LagInMS:      90000,
				RegisteredAt: 123,
				Member:       model.FederationMember{RemoteRepoKey: state},
			},
		)
	}
	api := federationhttp.FederatedAPI{Monitoring: service}
	summary, err := api.GetFederationStateSummaries(t.Context(), artgen.GetFederationStateSummariesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	counts := summary.(artgen.GetFederationStateSummaries200JSONResponse).FederationStates
	if *counts.NOTAVAILABLE != 2 || *counts.HEALTHY != 1 || *counts.DELAYED != 1 || *counts.FULLSYNCRUNNING != 1 || *counts.ERROR != 1 || *counts.DISABLED != 1 ||
		*counts.PENDINGFS != 1 {
		t.Fatalf("counts %+v", counts)
	}
	filtered, err := api.GetFederationStateDetails(
		t.Context(),
		artgen.GetFederationStateDetailsRequestObject{Params: artgen.GetFederationStateDetailsParams{Status: httpkit.PtrTo("NOT_AVAILABLE,ERROR")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	rows := *filtered.(artgen.GetFederationStateDetails200JSONResponse).Federations
	if len(rows) != 1 || len(*rows[0].MemberInfoList) != 3 || *rows[0].Priority != "HIGH" {
		t.Fatalf("filter %+v", rows)
	}
	_, err = api.GetFederationStateDetails(
		t.Context(),
		artgen.GetFederationStateDetailsRequestObject{Params: artgen.GetFederationStateDetailsParams{Status: httpkit.PtrTo("not-a-status")}},
	)
	requireStatus(t, err, http.StatusBadRequest)
	response, err := api.GetFederationMirrorLagTime(
		t.Context(),
		artgen.GetFederationMirrorLagTimeRequestObject{Params: artgen.GetFederationMirrorLagTimeParams{ThresholdTimeInMs: httpkit.PtrTo(int64(90000))}},
	)
	if err != nil || len(response.(artgen.GetFederationMirrorLagTime200JSONResponse)) != 0 {
		t.Fatalf("threshold boundary: %v %v", response, err)
	}
	response, err = api.GetFederationMirrorLagTime(t.Context(), artgen.GetFederationMirrorLagTimeRequestObject{})
	if err != nil || len(response.(artgen.GetFederationMirrorLagTime200JSONResponse)) != 8 {
		t.Fatalf("lag report: %v %v", response, err)
	}
	unavailable, err := api.GetUnavailableMirrors(t.Context(), artgen.GetUnavailableMirrorsRequestObject{})
	if err != nil || len(unavailable.(artgen.GetUnavailableMirrors200JSONResponse)) != 6 {
		t.Fatalf("unavailable: %v %v", unavailable, err)
	}
	states, err := api.GetFederationSyncState(t.Context(), artgen.GetFederationSyncStateRequestObject{})
	if err != nil || *states.(artgen.GetFederationSyncState200JSONResponse)[0].Status != "OUT_OF_SYNC" {
		t.Fatalf("states: %v %v", states, err)
	}
}

func TestUnitMonitoringAdapterKeepsAuthorityBeforeSelectorRefusals(t *testing.T) {
	service := &monitoringService{err: kernel.ErrForbidden}
	api := federationhttp.FederatedAPI{Monitoring: service}
	request := artgen.GetFederatedRepositoryStatusProjectionRequestObject{RepoKey: "missing", FederatedKey: "not-a-selector"}
	_, err := api.GetFederatedRepositoryStatusProjection(t.Context(), request)
	if !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("authority changed: %v", err)
	}
	service.err = federated.ErrNoRepository
	_, err = api.GetFederatedRepositoryStatusProjection(t.Context(), request)
	requireStatus(t, err, http.StatusNotFound)
	service.err = nil
	_, err = api.GetFederatedRepositoryStatusProjection(t.Context(), request)
	requireStatus(t, err, http.StatusBadRequest)
	api.Monitoring = nil
	_, err = api.GetFederatedRepositoryStatus(t.Context(), artgen.GetFederatedRepositoryStatusRequestObject{})
	requireStatus(t, err, http.StatusNotImplemented)
}

func TestUnitMonitoringRTFSKeepsReceiverRoleSeparateFromHealth(t *testing.T) {
	service := &monitoringService{view: federated.RepositoryStatus{Key: "fed", Members: []federated.MemberStatus{{
		Status: "HEALTHY", Mode: "Receiving_Only", Supported: true,
	}}}}
	api := federationhttp.FederatedAPI{Monitoring: service}
	response, err := api.GetFederationStateSummaries(t.Context(), artgen.GetFederationStateSummariesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	counts := response.(artgen.GetFederationStateSummaries200JSONResponse).FederationStates
	if counts.READONLY == nil || *counts.READONLY != 0 || *counts.HEALTHY != 1 {
		t.Fatalf("receiver changed health classification: %+v", counts)
	}
	filtered, err := api.GetFederationStateDetails(t.Context(), artgen.GetFederationStateDetailsRequestObject{
		Params: artgen.GetFederationStateDetailsParams{Status: httpkit.PtrTo("READ_ONLY")},
	})
	if err != nil || len(*filtered.(artgen.GetFederationStateDetails200JSONResponse).Federations) != 0 {
		t.Fatalf("read-only state filter: %+v %v", filtered, err)
	}
}

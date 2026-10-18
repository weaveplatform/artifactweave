package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

// GetStorageSummaryInfo answers the storage summary to an administrator or
// to a token scoped system:info/storage:r, as 7.x's getstoragesummaryinfo
// page allows. It is computed on every request, so it never lags writes.
func (s artifactoryService) GetStorageSummaryInfo(
	ctx context.Context,
	_ artgen.GetStorageSummaryInfoRequestObject,
) (artgen.GetStorageSummaryInfoResponseObject, error) {
	p, err := auth.RequireAuthenticated(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // an apierror value
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	a, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, s.internalError(ctx, "storage summary access", err)
	}
	if !a.d.System("info/storage", types.ScopeRead) {
		return nil, auth.ErrForbidden
	}
	sum, err := usage.Summarize(ctx, st, s.cfg.BlobLocation)
	if err != nil {
		return nil, s.internalError(ctx, "storage summary", err)
	}
	if s.cfg.Mirrors == nil {
		return artgen.GetStorageSummaryInfo200JSONResponse(storageSummary(sum)), nil
	}
	mirrors, err := s.cfg.Mirrors.Status(ctx)
	if err != nil {
		return nil, s.internalError(ctx, "mirror status", err)
	}
	return summaryWithMirrors{storageSummary(sum), mirrorSummaries(mirrors)}, nil
}

// OptimizeSystemStorage starts a repair pass on every blob mirror at once,
// as Artifactory's optimize starts its sharding repair (7.104.5). Admin
// only. The acknowledgement texts are artifactweave's: the spec documents
// only a plain-text body.
func (s artifactoryService) OptimizeSystemStorage(
	ctx context.Context,
	_ artgen.OptimizeSystemStorageRequestObject,
) (artgen.OptimizeSystemStorageResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	if s.cfg.Mirrors == nil {
		return artgen.OptimizeSystemStorage200TextResponse(
			"No blob mirror is configured; nothing to optimize"), nil
	}
	s.cfg.Mirrors.Optimize()
	return artgen.OptimizeSystemStorage200TextResponse("Blob mirror repair started"), nil
}

// summaryWithMirrors is the storage summary plus blobMirrors, an
// artifactweave extension (decision 0028): Artifactory reports nothing per
// shard, and JFrog clients ignore fields they do not know. Both embedded
// documents are flattened into one JSON object.
type summaryWithMirrors struct {
	artgen.StorageSummaryInfo
	wire.StorageSummaryMirrors
}

func (r summaryWithMirrors) VisitGetStorageSummaryInfoResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r) //nolint:wrapcheck // the response is already committed
}

func mirrorSummaries(mirrors []MirrorStatus) wire.StorageSummaryMirrors {
	out := make([]wire.BlobMirror, 0, len(mirrors))
	for _, m := range mirrors {
		bm := wire.BlobMirror{
			Tier: m.Tier, Zone: m.Zone, Redundancy: m.Redundancy, WriteQuorum: m.WriteQuorum,
			UnderReplicatedBlobs: m.UnderReplicated,
			Members:              make([]wire.BlobMirrorMember, 0, len(m.Members)),
			RemovedMembers:       make([]wire.BlobMirrorRemovedMember, 0, len(m.Removed)),
		}
		for _, mem := range m.Members {
			bm.Members = append(bm.Members, wire.BlobMirrorMember{
				Name:             mem.Name,
				Zone:             mem.Zone,
				Backend:          mem.Kind,
				WriteEnabled:     mem.WriteEnabled,
				Available:        mem.Available,
				AverageLatencyMs: mem.Latency.Milliseconds(),
				Replicas:         replicaTallies(mem.Replicas),
				SoleCopies:       mem.SoleCopies,
			})
		}
		for _, rm := range m.Removed {
			bm.RemovedMembers = append(bm.RemovedMembers, wire.BlobMirrorRemovedMember{
				Name: rm.Name, Replicas: replicaTallies(rm.Replicas), SoleCopies: rm.SoleCopies,
			})
		}
		out = append(out, bm)
	}
	return wire.StorageSummaryMirrors{BlobMirrors: out}
}

// replicaTallies keys a member's replica counts by state, with all four
// states present.
func replicaTallies(counts []store.ReplicaCount) map[string]wire.ReplicaTally {
	reps := map[string]wire.ReplicaTally{}
	for _, st := range []types.ReplicaState{
		types.ReplicaPresent, types.ReplicaPending, types.ReplicaDeleting, types.ReplicaFailed,
	} {
		reps[string(st)] = wire.ReplicaTally{Size: humanBytes(0)}
	}
	for _, c := range counts {
		reps[string(c.State)] = wire.ReplicaTally{
			Count: c.Count, Bytes: c.Bytes, Size: humanBytes(c.Bytes),
		}
	}
	return reps
}

// RefreshStorageSummaryInfo acknowledges a recalculation. The summary is
// computed on every request, so there is nothing to wait for.
func (artifactoryService) RefreshStorageSummaryInfo(
	ctx context.Context,
	_ artgen.RefreshStorageSummaryInfoRequestObject,
) (artgen.RefreshStorageSummaryInfoResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	return artgen.RefreshStorageSummaryInfo202TextResponse("Storage summary calculated"), nil
}

// notApplicable is what Artifactory's NumberFormatter prints for a
// percentage of nothing, where Java's division gives NaN.
const notApplicable = "N/A"

// storageSummary renders the summary with Artifactory's human-formatted
// strings, which the JFrog CLI prints verbatim.
func storageSummary(sum usage.Summary) artgen.StorageSummaryInfo {
	str := func(s string) *string { return &s }
	num := func(n int64) *int { v := int(n); return &v }
	// A 7.x object store reports no space fields at all (the SaaS capture in
	// research 15), so neither does a store whose volume is not measured.
	fileStore := artgen.FileStoreSummary{
		StorageType: str(sum.Location.Type), StorageDirectory: str(sum.Location.Directory),
	}
	if sp := sum.Space; sp != nil && sp.Total > 0 {
		used := sp.Total - sp.Free
		fileStore.TotalSpace = str(humanBytes(sp.Total))
		fileStore.UsedSpace = str(fmt.Sprintf("%s (%s)", humanBytes(used), percent(used, sp.Total)))
		fileStore.FreeSpace = str(
			fmt.Sprintf("%s (%s)", humanBytes(sp.Free), percent(sp.Free, sp.Total)),
		)
	}
	rows := make([]artgen.RepositorySummary, 0, len(sum.Rows)+1)
	var files, folders, bytes int64
	for _, r := range sum.Rows {
		files, folders, bytes = files+r.Files, folders+r.Folders, bytes+r.Bytes
	}
	for _, r := range sum.Rows {
		packageType := "NA"
		if r.PackageType != "" {
			packageType = repo.PackageTypeDisplayName(string(r.PackageType))
		}
		rows = append(rows, artgen.RepositorySummary{
			RepoKey: str(r.Key), RepoType: str(r.Type), PackageType: str(packageType),
			ProjectKey: str(r.ProjectKey), FilesCount: num(r.Files), FoldersCount: num(r.Folders),
			ItemsCount: num(r.Files + r.Folders), UsedSpace: str(humanBytes(r.Bytes)),
			UsedSpaceInBytes: &r.Bytes, Percentage: str(percent(r.Bytes, bytes)),
		})
	}
	// 7.x's TOTAL row has no packageType, projectKey or percentage (the
	// captures in research 15).
	rows = append(rows, artgen.RepositorySummary{
		RepoKey: str("TOTAL"), RepoType: str("NA"),
		FilesCount: num(files), FoldersCount: num(folders), ItemsCount: num(files + folders),
		UsedSpace: str(humanBytes(bytes)), UsedSpaceInBytes: &bytes,
	})
	return artgen.StorageSummaryInfo{
		BinariesSummary: &artgen.BinariesSummary{
			BinariesCount:  str(thousands(sum.BinariesCount)),
			BinariesSize:   str(humanBytes(sum.BinariesSize)),
			ArtifactsCount: str(thousands(sum.ArtifactsCount)),
			ArtifactsSize:  str(humanBytes(sum.ArtifactsSize)),
			ItemsCount:     str(thousands(sum.ItemsCount)),
			Optimization: str(
				percent(sum.ArtifactsSize-sum.BinariesSize, sum.ArtifactsSize),
			),
		},
		FileStoreSummary:        &fileStore,
		RepositoriesSummaryList: &rows,
	}
}

// humanBytes formats n as Artifactory's StorageUnit does: whole bytes below
// 1 KB, and otherwise two decimals with thousands separators in binary units
// ("1.00 KB", "1,004.64 KB", "10.00 MB"). TB is the largest unit, as in
// every 7.x capture, and the largest jfrog-cli-core parses when it falls
// back to usedSpace.
func humanBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	v, unit := float64(n)/1024, "KB"
	for _, u := range []string{"MB", "GB", "TB"} {
		if v < 1024 {
			break
		}
		v, unit = v/1024, u
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(v, 'f', 2, 64), ".")
	w, _ := strconv.ParseInt(whole, 10, 64) // FormatFloat wrote the digits, so they parse
	return thousands(w) + "." + frac + " " + unit
}

// thousands formats n with comma separators ("125,726").
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// percent formats part of whole as Artifactory's NumberFormatter does: up to
// two decimals with trailing zeros dropped ("2.08%", "0.1%", "0%"), and
// notApplicable for a share of nothing, as on an empty instance.
func percent(part, whole int64) string {
	if whole <= 0 {
		return notApplicable
	}
	s := strconv.FormatFloat(float64(part)*100/float64(whole), 'f', 2, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".") + "%"
}

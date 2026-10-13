package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
)

// GetStorageSummaryInfo answers the storage summary. Admin only. It is
// computed on every request, so it never lags writes.
func (s artifactoryService) GetStorageSummaryInfo(
	ctx context.Context,
	_ artgen.GetStorageSummaryInfoRequestObject,
) (artgen.GetStorageSummaryInfoResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	sum, err := usage.Summarize(ctx, st, s.cfg.BlobLocation)
	if err != nil {
		return nil, s.internalError(ctx, "storage summary", err)
	}
	return artgen.GetStorageSummaryInfo200JSONResponse(storageSummary(sum)), nil
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

// storageSummary renders the summary with Artifactory's human-formatted
// strings, which the JFrog CLI prints verbatim.
func storageSummary(sum usage.Summary) artgen.StorageSummaryInfo {
	str := func(s string) *string { return &s }
	num := func(n int64) *int { v := int(n); return &v }
	optimization := "0%"
	if sum.ArtifactsSize > 0 {
		optimization = percent(sum.ArtifactsSize-sum.BinariesSize, sum.ArtifactsSize)
	}
	fileStore := artgen.FileStoreSummary{
		StorageType: str(sum.Location.Type), StorageDirectory: str(sum.Location.Directory),
		TotalSpace: str("unknown"), UsedSpace: str("unknown"), FreeSpace: str("unknown"),
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
		rows = append(rows, artgen.RepositorySummary{
			RepoKey: str(r.Key), RepoType: str(r.Type), PackageType: str(string(r.PackageType)),
			FilesCount: num(r.Files), FoldersCount: num(r.Folders),
			ItemsCount: num(r.Files + r.Folders), UsedSpace: str(humanBytes(r.Bytes)),
			Percentage: str(percent(r.Bytes, bytes)),
		})
	}
	rows = append(rows, artgen.RepositorySummary{
		RepoKey: str("TOTAL"), RepoType: str("NA"), PackageType: str("NA"),
		FilesCount: num(files), FoldersCount: num(folders), ItemsCount: num(files + folders),
		UsedSpace: str(humanBytes(bytes)), Percentage: str("100%"),
	})
	return artgen.StorageSummaryInfo{
		BinariesSummary: &artgen.BinariesSummary{
			BinariesCount:  str(thousands(sum.BinariesCount)),
			BinariesSize:   str(humanBytes(sum.BinariesSize)),
			ArtifactsCount: str(thousands(sum.ArtifactsCount)),
			ArtifactsSize:  str(humanBytes(sum.ArtifactsSize)),
			ItemsCount:     str(thousands(sum.ItemsCount)),
			Optimization:   str(optimization),
		},
		FileStoreSummary:        &fileStore,
		RepositoriesSummaryList: &rows,
	}
}

// humanBytes formats n with binary units and up to two decimals, as
// Artifactory does ("1 KB", "3.48 GB").
func humanBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	v := float64(n)
	unit := ""
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	return s + " " + unit
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

// percent formats part of whole as "nn.nn%", with trailing zeros dropped.
func percent(part, whole int64) string {
	if whole <= 0 {
		return "0%"
	}
	s := strconv.FormatFloat(float64(part)*100/float64(whole), 'f', 2, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".") + "%"
}

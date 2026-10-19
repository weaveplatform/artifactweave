package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestStorageSummaryAccess: an administrator, or a token scoped
// system:info/storage:r, reads the summary (getstoragesummaryinfo); the
// recalculation stays with administrators.
func TestStorageSummaryAccess(t *testing.T) {
	w := newIdentityWorld(t)
	if err := w.db.Users().Create(context.Background(), types.User{
		Name: "watcher", PasswordHash: cheapHash,
	}); err != nil {
		t.Fatal(err)
	}
	const (
		info = "/artifactory/api/storageinfo"
		calc = "/artifactory/api/storageinfo/calculate"
	)
	for _, c := range []struct {
		name  string
		creds map[string]string
		want  int
	}{
		{"admin", admin, http.StatusOK},
		{"storage scope", w.bearer(t, "watcher", "system:info/storage:r"), http.StatusOK},
		// A scope on the whole info area covers its storage part.
		{"info scope", w.bearer(t, "watcher", "system:info:r"), http.StatusOK},
		{"other system area", w.bearer(t, "watcher", "system:metrics:r"), http.StatusForbidden},
		{"user's own token", w.bearer(t, "watcher", ""), http.StatusForbidden},
		{"password", basic("watcher", "pw"), http.StatusForbidden},
		{"no credentials", nil, http.StatusUnauthorized},
	} {
		if rec := do(t, w.h, http.MethodGet, info, c.creds, ""); rec.Code != c.want {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	scoped := w.bearer(t, "watcher", "system:info/storage:r")
	if rec := do(t, w.h, http.MethodPost, calc, scoped, ""); rec.Code != http.StatusForbidden {
		t.Errorf("calculate with the storage scope: %d %s", rec.Code, rec.Body)
	}
}

// A failure reading the caller's permissions is a server error, not a
// refusal.
func TestStorageSummaryAccessFailure(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = faultyStore{Store: db, fail: map[string]bool{"permissions": true}}
	})
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("user", "pw"), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a permission read that fails: %d %s", rec.Code, rec.Body)
	}
}

// TestStorageSummaryLikeAnEmpty7x renders the summary of an instance with
// nothing stored and compares it with a 7.x SaaS capture of one
// (watsonmartin/jartifactory README): N/A for every share, an auto-trashcan
// row, a TOTAL row without packageType, projectKey or percentage, and an
// object store with no space fields. Only the directory differs: SaaS
// reports "Artifactory Cloud", and a store with no local copy reports what
// a self-hosted chain without a filesystem provider does (#461).
func TestStorageSummaryLikeAnEmpty7x(t *testing.T) {
	sum := storageSummary(usage.Summary{
		Location: usage.Location{Type: "s3-storage-v3-direct", Directory: usage.NoFilesystem},
		Rows: []usage.Row{
			{
				Key: "artifactory-build-info", Type: "LOCAL", PackageType: repo.BuildInfo,
				ProjectKey: "default",
			},
			{Key: "auto-trashcan", Type: "NA", ProjectKey: "default"},
			{
				Key: "example-repo-local", Type: "LOCAL", PackageType: repo.Generic,
				ProjectKey: "default",
			},
		},
	})
	body, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	row := func(key, repoType, packageType string) map[string]any {
		return map[string]any{
			"repoKey": key, "repoType": repoType, "foldersCount": 0.0, "filesCount": 0.0,
			"usedSpace": "0 bytes", "usedSpaceInBytes": 0.0, "itemsCount": 0.0,
			"packageType": packageType, "projectKey": "default", "percentage": "N/A",
		}
	}
	want := map[string]any{
		"binariesSummary": map[string]any{
			"binariesCount": "0", "binariesSize": "0 bytes", "artifactsSize": "0 bytes",
			"optimization": "N/A", "itemsCount": "0", "artifactsCount": "0",
		},
		"fileStoreSummary": map[string]any{
			"storageType":      "s3-storage-v3-direct",
			"storageDirectory": "Filesystem storage is not used",
		},
		"repositoriesSummaryList": []any{
			row("artifactory-build-info", "LOCAL", "BuildInfo"),
			row("auto-trashcan", "NA", "NA"),
			row("example-repo-local", "LOCAL", "Generic"),
			map[string]any{
				"repoKey": "TOTAL", "repoType": "NA", "foldersCount": 0.0, "filesCount": 0.0,
				"usedSpace": "0 bytes", "usedSpaceInBytes": 0.0, "itemsCount": 0.0,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary\n got %s", body)
	}
}

// TestStorageSummaryOptimization: optimization is binariesSize over
// artifactsSize (#461), as JFrog's getstoragesummaryinfo example (3.48 GB
// over 59.77 GB, 5.82%) and the bundle capture (67.84 GB over 83.64 GB,
// 81.1%) show; N/A while there are no artifacts.
func TestStorageSummaryOptimization(t *testing.T) {
	for _, tc := range []struct {
		binaries, artifacts int64
		want                string
	}{
		{348, 5977, "5.82%"},
		{6784, 8364, "81.11%"},
		{100, 100, "100%"},
		{0, 0, "N/A"},
	} {
		sum := storageSummary(usage.Summary{BinariesSize: tc.binaries, ArtifactsSize: tc.artifacts})
		if got := *sum.BinariesSummary.Optimization; got != tc.want {
			t.Errorf("%d over %d: %s, want %s", tc.binaries, tc.artifacts, got, tc.want)
		}
	}
}

// TestStorageSummaryShares checks a populated summary against the shape of
// a self-hosted 7.x capture (research 15): shares of the total with up to
// two decimals, the trash can counted like a repository, and a TOTAL row
// that sums every row.
func TestStorageSummaryShares(t *testing.T) {
	sum := storageSummary(usage.Summary{
		Location: usage.Location{Type: "file-system", Directory: "/data"},
		Space:    &usage.Space{Total: 4 << 30, Free: 1 << 30},
		Rows: []usage.Row{
			{
				Key:        "auto-trashcan",
				Type:       "NA",
				ProjectKey: "default",
				Files:      1,
				Folders:    2,
				Bytes:      1,
			},
			{
				Key: "big", Type: "LOCAL", PackageType: repo.Docker, ProjectKey: "proj", Files: 3,
				Folders: 1, Bytes: 998,
			},
			{Key: "none", Type: "VIRTUAL", PackageType: repo.NPM, ProjectKey: "default"},
			{
				Key:         "tiny",
				Type:        "LOCAL",
				PackageType: repo.RPM,
				ProjectKey:  "default",
				Files:       1,
				Bytes:       1,
			},
		},
	})
	rows := *sum.RepositoriesSummaryList
	shares := map[string]string{}
	for _, r := range rows[:len(rows)-1] {
		shares[*r.RepoKey] = *r.PackageType + " " + *r.ProjectKey + " " + *r.Percentage
	}
	if !reflect.DeepEqual(shares, map[string]string{
		"auto-trashcan": "NA default 0.1%",
		"big":           "Docker proj 99.8%",
		"none":          "npm default 0%",
		"tiny":          "RPM default 0.1%",
	}) {
		t.Fatalf("shares %v", shares)
	}
	total := rows[len(rows)-1]
	if *total.RepoKey != "TOTAL" || *total.FilesCount != 5 || *total.FoldersCount != 3 ||
		*total.ItemsCount != 8 || *total.UsedSpaceInBytes != 1000 || *total.UsedSpace != "1000 bytes" ||
		total.PackageType != nil || total.ProjectKey != nil || total.Percentage != nil {
		t.Fatalf("total %+v", total)
	}
	fs := sum.FileStoreSummary
	if *fs.TotalSpace != "4.00 GB" || *fs.UsedSpace != "3.00 GB (75%)" ||
		*fs.FreeSpace != "1.00 GB (25%)" {
		t.Fatalf("file store %+v", fs)
	}
}

// The minimal shapes JFrog's clients decode the summary into, copied from
// jfrog-client-go 3b4b900d (artifactory/services/utils/storageutils.go),
// whose json.Number fields take a number or a numeric string, and from
// ps-jfrog/jfrog-ps-cli (internal/discover/storage.go), whose int64 takes a
// number only.
type (
	// jfrog-client-go embeds the two summaries; a tagged embedded struct
	// decodes as a named field, which is how this copy declares them.
	jfrogStorageInfo struct {
		Binaries                jfrogBinariesSummary     `json:"binariesSummary,omitempty"`
		RepositoriesSummaryList []jfrogRepositorySummary `json:"repositoriesSummaryList,omitempty"`
		FileStore               jfrogFileStoreSummary    `json:"fileStoreSummary,omitempty"`
	}
	jfrogBinariesSummary struct {
		BinariesCount  string `json:"binariesCount,omitempty"`
		BinariesSize   string `json:"binariesSize,omitempty"`
		ArtifactsSize  string `json:"artifactsSize,omitempty"`
		Optimization   string `json:"optimization,omitempty"`
		ItemsCount     string `json:"itemsCount,omitempty"`
		ArtifactsCount string `json:"artifactsCount,omitempty"`
	}
	jfrogRepositorySummary struct {
		RepoKey          string      `json:"repoKey,omitempty"`
		RepoType         string      `json:"repoType,omitempty"`
		FoldersCount     json.Number `json:"foldersCount,omitempty"`
		FilesCount       json.Number `json:"filesCount,omitempty"`
		UsedSpace        string      `json:"usedSpace,omitempty"`
		UsedSpaceInBytes json.Number `json:"usedSpaceInBytes,omitempty"`
		ItemsCount       json.Number `json:"itemsCount,omitempty"`
		PackageType      string      `json:"packageType,omitempty"`
		ProjectKey       string      `json:"projectKey,omitempty"`
		Percentage       string      `json:"percentage,omitempty"`
	}
	jfrogFileStoreSummary struct {
		StorageType      string `json:"storageType,omitempty"`
		StorageDirectory string `json:"storageDirectory,omitempty"`
		TotalSpace       string `json:"totalSpace,omitempty"`
		UsedSpace        string `json:"usedSpace,omitempty"`
		FreeSpace        string `json:"freeSpace,omitempty"`
	}
	psRepoSummary struct {
		RepoKey          string `json:"repoKey"`
		RepoType         string `json:"repoType"`
		PackageType      string `json:"packageType"`
		FoldersCount     int    `json:"foldersCount"`
		FilesCount       int    `json:"filesCount"`
		ItemsCount       int    `json:"itemsCount"`
		UsedSpaceInBytes int64  `json:"usedSpaceInBytes"`
		ProjectKey       string `json:"projectKey"`
	}
)

// usedSpaceInBytes is jfrog-cli-core's GetUsedSpaceInBytes
// (artifactory/utils/storageinfo.go), which transfer-files sizes each
// repository with: usedSpaceInBytes when sent, else usedSpace parsed.
func usedSpaceInBytes(r jfrogRepositorySummary) (int64, error) {
	if r.UsedSpaceInBytes.String() != "" {
		return r.UsedSpaceInBytes.Int64() //nolint:wrapcheck // a copy of the client's code
	}
	return parseStorageSize(r.UsedSpace)
}

// parseStorageSize is jfrog-cli-core's convertStorageSizeStringToBytes, the
// fallback above.
func parseStorageSize(s string) (int64, error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, errors.New("could not parse size string " + s)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(parts[0], ",", ""), 64)
	if err != nil {
		return 0, err //nolint:wrapcheck // a copy of the client's code
	}
	unit := map[string]float64{
		"bytes": 1,
		"KB":    1 << 10,
		"MB":    1 << 20,
		"GB":    1 << 30,
		"TB":    1 << 40,
	}
	mul, ok := unit[parts[1]]
	if !ok {
		return 0, errors.New("could not parse size string " + s)
	}
	return int64(v * mul), nil
}

// TestStorageSummaryInJFrogClients decodes a summary as jf transfer-files
// (jfrog-cli-core over jfrog-client-go) and JFrog's PS CLI do. A live run of
// transfer-files needs a source Artifactory with JFrog's data-transfer
// plugin, which the acceptance harness cannot provide, so the clients' own
// structs and parsers stand in for it.
func TestStorageSummaryInJFrogClients(t *testing.T) {
	sizes := map[repo.PackageType]int64{
		repo.Maven: 1028751, repo.Gradle: 10 << 20, repo.Ivy: 1, repo.SBT: 0,
		repo.Docker: 3_736_621_547, repo.Conan: 5 << 40, repo.NuGet: 1536, repo.NPM: 2048,
	}
	var rows []usage.Row
	for pt, n := range sizes {
		rows = append(rows, usage.Row{
			Key: string(pt) + "-local", Type: "LOCAL", PackageType: pt, ProjectKey: "default",
			Files: 1, Folders: 1, Bytes: n,
		})
	}
	body, err := json.Marshal(storageSummary(usage.Summary{
		BinariesCount: 1, BinariesSize: 1, ArtifactsCount: 8, ArtifactsSize: 1, ItemsCount: 16,
		Location: usage.Location{Type: "file-system", Directory: "/data"},
		Rows:     rows,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var info jfrogStorageInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	// jfrog-client-go's live storageInfoTest asserts these are not empty.
	bin, fs := info.Binaries, info.FileStore
	if bin.BinariesCount == "" || bin.BinariesSize == "" || bin.ArtifactsSize == "" ||
		bin.Optimization == "" || bin.ItemsCount == "" || bin.ArtifactsCount == "" ||
		fs.StorageType == "" || fs.StorageDirectory == "" {
		t.Fatalf("summary %s", body)
	}
	// transfer-files orders uploads by these spellings (delayedartifactshandler.go).
	transferNames := map[repo.PackageType]string{
		repo.Maven: "Maven", repo.Gradle: "Gradle", repo.Ivy: "Ivy", repo.SBT: "SBT",
		repo.Docker: "Docker", repo.Conan: "Conan", repo.NuGet: "NuGet", repo.NPM: "npm",
	}
	for _, r := range info.RepositoriesSummaryList {
		if r.RepoKey == "TOTAL" {
			continue
		}
		pt := repo.PackageType(strings.TrimSuffix(r.RepoKey, "-local"))
		want := sizes[pt]
		if got, err := usedSpaceInBytes(r); err != nil || got != want {
			t.Errorf("%s: usedSpaceInBytes %d, %v, want %d", r.RepoKey, got, err, want)
		}
		// The parsed fallback agrees to the two decimals usedSpace keeps.
		parsed, err := parseStorageSize(r.UsedSpace)
		if err != nil || float64(parsed-want) > float64(want)/1e4+1 ||
			float64(want-parsed) > float64(want)/1e4+1 {
			t.Errorf("%s: usedSpace %q parses to %d, %v, want about %d", r.RepoKey, r.UsedSpace,
				parsed, err, want)
		}
		if files, err := r.FilesCount.Int64(); err != nil || files != 1 {
			t.Errorf("%s: filesCount %v, %v", r.RepoKey, r.FilesCount, err)
		}
		if r.RepoType == "" || r.FoldersCount == "" || r.UsedSpace == "" ||
			r.UsedSpaceInBytes == "" || r.ItemsCount == "" || r.ProjectKey == "" ||
			r.Percentage == "" || r.PackageType != transferNames[pt] {
			t.Errorf("row %+v", r)
		}
	}
	var ps struct {
		RepositoriesSummaryList []psRepoSummary `json:"repositoriesSummaryList"`
	}
	if err := json.Unmarshal(body, &ps); err != nil {
		t.Fatalf("the PS CLI reads usedSpaceInBytes as a number: %v", err)
	}
	if last := ps.RepositoriesSummaryList[len(ps.RepositoriesSummaryList)-1]; last.RepoKey != "TOTAL" ||
		last.UsedSpaceInBytes != 3_736_621_547+(5<<40)+(10<<20)+1028751+1+1536+2048 {
		t.Fatalf("total %+v", last)
	}
}

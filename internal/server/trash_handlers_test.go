package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/gc"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestTrashListingAndRestore(t *testing.T) {
	h := statusFixture(t, false)
	admin := basic("admin", "pw")
	if rec := do(
		t,
		h,
		http.MethodDelete,
		"/artifactory/libs-local/fixture/del.txt",
		admin,
		"",
	); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	tc := "/artifactory/api/storage/auto-trashcan"
	root := decodeObject(t, do(t, h, http.MethodGet, tc, admin, "").Body.String())
	if kids := root["children"].([]any); len(kids) != 1 || //nolint:forcetypeassert // fixture
		kids[0].(map[string]any)["uri"] != "/libs-local" { //nolint:forcetypeassert // fixture
		t.Fatalf("root %v", root)
	}
	folder := decodeObject(
		t,
		do(t, h, http.MethodGet, tc+"/libs-local/fixture", admin, "").Body.String(),
	)
	if folder["path"] != "/libs-local/fixture" ||
		len(folder["children"].([]any)) != 1 { //nolint:forcetypeassert // fixture
		t.Fatalf("folder %v", folder)
	}
	file := decodeObject(
		t,
		do(t, h, http.MethodGet, tc+"/libs-local/fixture/del.txt", admin, "").Body.String(),
	)
	props := file["properties"].(map[string]any) //nolint:forcetypeassert // fixture
	if file["size"] != "7" ||
		props["trash.deletedBy"].([]any)[0] != "admin" || //nolint:forcetypeassert // fixture
		props["trash.originalPath"].([]any)[0] != "fixture/del.txt" { //nolint:forcetypeassert // fixture
		t.Fatalf("file %v", file)
	}
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/trash/restore/libs-local/fixture/del.txt?to=libs-local",
		admin,
		"",
	)
	if rec.Code != http.StatusAccepted || rec.Body.String() != restoredMessage ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("restore: %d %q %s", rec.Code, rec.Body, rec.Header())
	}
	if rec := do(
		t,
		h,
		http.MethodGet,
		"/artifactory/libs-local/fixture/del.txt",
		admin,
		"",
	); rec.Body.String() != "fixture" {
		t.Fatalf("restored content: %d %q", rec.Code, rec.Body)
	}
	// The generated one-segment restore route answers the same way.
	do(t, h, http.MethodDelete, "/artifactory/libs-local/fixture/del.txt", admin, "")
	if rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/trash/restore/libs-local?to=libs-local",
		admin,
		"",
	); rec.Code != http.StatusAccepted ||
		rec.Body.String() != restoredMessage {
		t.Fatalf("one-segment restore: %d %q", rec.Code, rec.Body)
	}
}

func TestStorageInfoBody(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("admin", "pw"), "")
	info := decodeObject(t, rec.Body.String())
	bin := info["binariesSummary"].(map[string]any) //nolint:forcetypeassert // fixture
	rows := info["repositoriesSummaryList"].([]any) //nolint:forcetypeassert // fixture
	last := rows[len(rows)-1].(map[string]any)      //nolint:forcetypeassert // fixture
	// Relations rather than counts, so files added to the shared fixture by
	// other features do not break this test: the TOTAL row agrees with the
	// binaries summary, and the fixture's identical contents are stored once.
	artifacts, _ := strconv.Atoi(bin["artifactsCount"].(string)) //nolint:forcetypeassert // fixture
	binaries, _ := strconv.Atoi(bin["binariesCount"].(string))   //nolint:forcetypeassert // fixture
	if last["repoKey"] != "TOTAL" || last["filesCount"] != float64(artifacts) ||
		binaries < 1 || binaries >= artifacts ||
		!strings.HasSuffix(bin["optimization"].(string), "%") { //nolint:forcetypeassert // fixture
		t.Fatalf("summary %s", rec.Body)
	}
	found := false
	for _, r := range rows {
		if r.(map[string]any)["repoKey"] == "remote-private-cache" { //nolint:forcetypeassert // fixture
			found = true
		}
	}
	if !found {
		t.Fatalf("a remote repository is listed under its cache alias: %s", rec.Body)
	}
}

func TestZapMessage(t *testing.T) {
	h := statusFixture(t, false)
	rec := do(
		t,
		h,
		http.MethodPost,
		"/artifactory/api/zap/remote-private-cache",
		basic("admin", "pw"),
		"",
	)
	if decodeObject(t, rec.Body.String())["Message"] != "Completed zapping cache in path remote-private-cache" {
		t.Fatalf("zap: %d %s", rec.Code, rec.Body)
	}
}

func TestConfiguredCollector(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	c, err := gc.New(db, nil, gc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := artifactoryService{cfg: Config{Store: db, GC: c}}
	if got, err := s.collector(); err != nil || got != c {
		t.Fatalf("collector %p, %v", got, err)
	}
	if _, err := (artifactoryService{}).collector(); err == nil {
		t.Fatal("no store, no collector")
	}
}

func TestStorageFormatting(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 bytes", 1023: "1023 bytes", 1024: "1 KB", 1536: "1.5 KB",
		3_736_621_547: "3.48 GB", 5 << 50: "5 PB", 3 << 60: "3072 PB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 123: "123", 125726: "125,726", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
	if percent(1, 3) != "33.33%" || percent(1, 0) != "0%" || percent(4, 4) != "100%" {
		t.Error("percent")
	}
	sum := storageSummary(usage.Summary{Space: &usage.Space{Total: 4 << 30, Free: 1 << 30}})
	fs := sum.FileStoreSummary
	if *fs.TotalSpace != "4 GB" || *fs.UsedSpace != "3 GB (75%)" || *fs.FreeSpace != "1 GB (25%)" ||
		*sum.BinariesSummary.Optimization != "0%" {
		t.Fatalf("file store %+v", fs)
	}
}

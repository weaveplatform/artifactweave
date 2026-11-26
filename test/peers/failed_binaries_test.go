package peers_test

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

func TestAccFailedFederatedBinariesSQLite(t *testing.T) {
	t.Parallel()
	testFailedFederatedBinaries(t, peers.SQLite)
}

func TestAccFailedFederatedBinariesPostgres(t *testing.T) {
	t.Parallel()
	testFailedFederatedBinaries(t, peers.Postgres)
}

type failedBinary struct {
	RepoKey, Path, SHA1 string
	FailuresCount       int
}

func failedBinaries(t *testing.T, in *peers.Instance) []failedBinary {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, "/artifactory/api/federation/binaries/failures/list?repoKey=failed", "")
	var tasks []failedBinary
	if status != http.StatusOK {
		t.Fatalf("list failures %d %s", status, body)
	}
	if err := json.Unmarshal([]byte(body), &tasks); err != nil {
		t.Fatal(err)
	}
	return tasks
}

func binaryCall(t *testing.T, in *peers.Instance, method, path string) {
	t.Helper()
	status, body := in.Do(t, method, "/artifactory/api/federation/"+path, "")
	if status != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, status, body)
	}
}

// A still pushes metadata to B while B cannot reach A for content. The
// independent egress cut therefore exercises a failed blob source without
// preventing the stream cursor advancing through later deletes/replacements.
func testFailedFederatedBinaries(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 2, Driver: driver})
	a, b := c.Instances[0], c.Instances[1]
	pair(t, a, b)
	federate(t, c.Instances, "failed", "generic")
	c.Cut(1, 0)
	for _, p := range []string{"replay", "cancel", "deleted", "replaced"} {
		status, body := a.Do(t, http.MethodPut, "/artifactory/failed/"+p, "content-"+p)
		if status != http.StatusCreated {
			t.Fatalf("deploy %s %d %s", p, status, body)
		}
	}
	eventually(t, b, "four failed fetches", func() (bool, string) {
		tasks := failedBinaries(t, b)
		raw, _ := json.Marshal(tasks)
		return len(tasks) == 4, string(raw)
	})
	tasks := failedBinaries(t, b)
	for _, task := range tasks {
		if task.RepoKey != "failed" || task.Path == "" || task.SHA1 == "" || task.FailuresCount < 1 {
			t.Fatalf("task %+v", task)
		}
	}
	for _, path := range []string{"binaries/tasks/failed", "status/repoState/failed/binaries/"} {
		status, body := b.Do(t, http.MethodGet, "/artifactory/api/federation/"+path, "")
		if status != http.StatusOK || !strings.Contains(body, `"failedTasks":4`) {
			t.Fatalf("counts %d %s", status, body)
		}
	}
	sum := sha1.Sum([]byte("content-cancel")) // #nosec G401 -- protocol checksum, not a security primitive
	cancelSHA := hex.EncodeToString(sum[:])
	binaryCall(t, b, http.MethodDelete, "binaries/failures/delete?repoKey=failed&sha1="+cancelSHA)
	if got := failedBinaries(t, b); len(got) != 3 {
		t.Fatalf("cancel left %+v", got)
	}
	// Later events win even if their original content still has a failed task.
	status, body := a.Do(t, http.MethodDelete, "/artifactory/failed/deleted", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete %d %s", status, body)
	}
	status, body = a.Do(t, http.MethodPut, "/artifactory/failed/replaced", "new-content")
	if status != http.StatusCreated {
		t.Fatalf("replace %d %s", status, body)
	}
	// Superseded failures may already have been cleared by automatic retry;
	// wait for the replacement content's checksum rather than their count.
	replacement := sha1.Sum([]byte("new-content")) // #nosec G401 -- protocol checksum
	replacementSHA := hex.EncodeToString(replacement[:])
	eventually(t, b, "replacement failed task", func() (bool, string) {
		tasks := failedBinaries(t, b)
		for _, task := range tasks {
			if task.SHA1 == replacementSHA {
				return true, task.Path
			}
		}
		raw, _ := json.Marshal(tasks)
		return false, string(raw)
	})
	c.Restore(1, 0)
	binaryCall(t, b, http.MethodPut, "binaries/failures/replay?repoKey=failed")
	binaryCall(t, b, http.MethodPut, "binaries/failures/replay?repoKey=failed")
	awaitFile(t, []*peers.Instance{b}, "failed/replay", "content-replay")
	awaitFile(t, []*peers.Instance{b}, "failed/replaced", "new-content")
	absent(t, []*peers.Instance{b}, "failed/deleted")
	absent(t, []*peers.Instance{b}, "failed/cancel")
	eventually(t, b, "no remaining failed fetches", func() (bool, string) { return len(failedBinaries(t, b)) == 0, "failed tasks remain" })
	// No newer content event is needed: a normal full sync must repair the
	// missing binary even though the receiver already has its winning stamp.
	binaryCall(t, a, http.MethodPost, "fullSync/failed")
	awaitFile(t, []*peers.Instance{b}, "failed/cancel", "content-cancel")
}

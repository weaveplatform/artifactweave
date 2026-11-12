package source

import (
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// TestUnitRefusalsExpireAndStayBounded: a remembered refusal lasts the
// repository's missedRetrievalCachePeriodSecs, 1800 when unset, and a full
// memory drops what has expired first, then an arbitrary refusal.
func TestUnitRefusalsExpireAndStayBounded(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	r := &refusals{now: func() time.Time { return now }}
	id := model.SourceIdentity{ID: "s"}
	refused := &MIMERefusal{RejectionError: &format.RejectionError{Status: 409, Message: "m"}}
	ten := 10
	r.put(repo.Config{Key: "up", MissedRetrievalCachePeriodSecs: &ten}, id, "a", refused)
	r.put(repo.Config{Key: "up"}, id, "b", refused)
	now = now.Add(9 * time.Second)
	if r.get("up", id, "a") != refused || r.get("up", id, "b") != refused {
		t.Fatal("forgotten before the period passed")
	}
	if r.get("up", model.SourceIdentity{ID: "other"}, "a") != nil {
		t.Fatal("answered for another source identity")
	}
	now = now.Add(time.Second)
	if r.get("up", id, "a") != nil || r.get("up", id, "b") != refused {
		t.Fatal("ten seconds: a should expire and b, at the default 1800, stay")
	}
	now = now.Add(1800 * time.Second)
	if r.get("up", id, "b") != nil {
		t.Fatal("kept past the default period")
	}

	r = &refusals{now: func() time.Time { return now }}
	r.put(repo.Config{Key: "up", MissedRetrievalCachePeriodSecs: &ten}, id, "expired", refused)
	now = now.Add(time.Minute)
	for i := range maxRefusals - 1 {
		r.put(repo.Config{Key: "up"}, id, fmt.Sprint(i), refused)
	}
	r.put(repo.Config{Key: "up"}, id, "full", refused)
	if len(r.entries) != maxRefusals ||
		r.entries[refusalKey{repo: "up", identity: id, path: "expired"}].err != nil {
		t.Fatalf("%d refusals, the expired one kept", len(r.entries))
	}
	r.put(repo.Config{Key: "up"}, id, "over", refused)
	if len(r.entries) != maxRefusals || r.get("up", id, "over") != refused {
		t.Fatalf("%d refusals, want %d with the newest", len(r.entries), maxRefusals)
	}
}

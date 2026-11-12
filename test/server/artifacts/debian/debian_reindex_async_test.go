package debian_test

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// asyncRefused is the binder's answer to an async value strconv.ParseBool
// does not read, as it was before async was declared an integer.
const asyncRefused = `{"errors":[{"message":"Invalid format for parameter async: ` +
	`error binding string parameter: strconv.ParseBool: parsing \"2\": invalid syntax",` +
	`"status":400}]}` + "\n"

// TestAccDebianReindexAsync: Calculate Debian Repository Metadata takes
// async as JFrog declares it, 0 or 1, and as JFrog's earlier REST API
// reference wrote it, false or true. 1 or true queues the rebuild and
// answers at once, so the work is still queued; 0, false or no async runs
// it before the answer, so nothing is left queued. Each answers the same
// plain text. async=2 answers 400 and queues nothing [I]: no source says
// how 7.x answers a value it does not declare.
func TestAccDebianReindexAsync(t *testing.T) {
	ctx := context.Background()
	const key = "deb-local"
	world := func(t *testing.T) *itemWorld {
		w := newItemWorld(t, false)
		c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.Debian}
		if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		return w
	}
	for _, tc := range []struct {
		query  string
		queued bool
	}{
		{"", false},
		{"?async=0", false},
		{"?async=1", true},
		{"?async=false", false},
		{"?async=true", true},
	} {
		t.Run(cmp.Or(tc.query, "without async"), func(t *testing.T) {
			w := world(t)
			rec := w.do(http.MethodPost, "/artifactory/api/deb/reindex/"+key+tc.query, "admin", "")
			if rec.Code != http.StatusOK ||
				rec.Body.String() != "Debian repository deb-local metadata recalculation accepted" ||
				!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("answered %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
			}
			_, err := w.db.WorkQueue().Get(ctx, format.KindReindex, key)
			if tc.queued && err != nil {
				t.Errorf("not queued after the answer: %v", err)
			}
			if !tc.queued && !errors.Is(err, kernel.ErrNotFound) {
				t.Errorf("still queued after the answer: %v", err)
			}
		})
	}
	t.Run("?async=2", func(t *testing.T) {
		w := world(t)
		rec := w.do(http.MethodPost, "/artifactory/api/deb/reindex/"+key+"?async=2", "admin", "")
		if rec.Code != http.StatusBadRequest || rec.Body.String() != asyncRefused {
			t.Fatalf("answered %d %q", rec.Code, rec.Body)
		}
		if _, err := w.db.WorkQueue().Get(ctx, format.KindReindex, key); !errors.Is(
			err, kernel.ErrNotFound,
		) {
			t.Errorf("queued after a refusal: %v", err)
		}
	})
}

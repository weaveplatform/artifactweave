package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccSmartRefreshRetriesParkAndZapRecovery(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	entry := w.smartEntry()
	key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.status = http.StatusServiceUnavailable
	w.mu.Unlock()
	for attempt := 1; attempt <= 3; attempt++ {
		if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); err == nil {
			t.Fatal("outage accepted")
		}
		job, e := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
		if e != nil || job.Attempts != attempt {
			t.Fatal(job, e)
		}
		if attempt == 3 && job.State != types.WorkParked {
			t.Fatal("unbounded retries", job)
		}
	}
	if _, err = w.s.enqueue(
		w.ctx,
		w.smartRequest(),
		w.permit,
		entry.Token.Key,
		false,
	); !errors.Is(
		err,
		source.ErrUnavailable,
	) {
		t.Fatal("ordinary read revived parked work", err)
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); !errors.Is(err, work.ErrParked) {
		t.Fatal(err)
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if before != after {
		t.Fatal("parked work performed network I/O")
	}
	if _, err = w.db.RemoteCache().Invalidate(w.ctx, w.c.Key, "file.bin", true); err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false); err != nil {
		t.Fatal("explicit zap failed to revive work", err)
	}
	w.mu.Lock()
	w.status = 0
	w.mu.Unlock()
	if err = w.s.queue.EnsureFresh(w.ctx, w.s.kind, key); err != nil {
		t.Fatal(err)
	}
}

func TestAccSmartDurableCapacityAndCorruptPayload(t *testing.T) {
	w := smartFixture(t)
	for n := range 256 {
		if err := w.db.WorkQueue().
			Enqueue(w.ctx, w.s.kind, fmt.Sprintf("pending-%d", n), nil, w.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.s.admit(w.ctx, "another"); !errors.Is(err, ErrBusy) {
		t.Fatal("unbounded admission", err)
	}
	if err := w.s.admit(w.ctx, "pending-1"); err != nil {
		t.Fatal("coalescing blocked", err)
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	for _, payload := range []string{"{", `{}`, `{"item_id":-1}`} {
		if err := w.s.run(
			w.ctx,
			types.WorkItem{Payload: []byte(payload)},
		); !errors.Is(
			err,
			work.ErrPermanent,
		) {
			t.Fatal(err)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.calls != before {
		t.Fatal("invalid job performed I/O")
	}
}

func TestAccSmartStatisticsLostAcknowledgementNeverReplays(t *testing.T) {
	for _, mode := range []string{"oversized", "truncated", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			w := smartFixture(t)
			result := w.smartRead()
			if err := result.Used(w.ctx); err != nil {
				t.Fatal(err)
			}
			entry := w.smartEntry()
			job, err := w.db.WorkQueue().
				Get(w.ctx, w.s.kind, smartWorkKey(entry.Token.Key, w.permit.Identity, true))
			if err != nil {
				t.Fatal(err)
			}
			sends := 0
			w.mu.Lock()
			w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "/statistics/") {
					return false
				}
				sends++
				switch mode {
				case "oversized":
					_, _ = io.WriteString(rw, strings.Repeat("x", (64<<10)+1))
				case "truncated":
					rw.Header().Set("Content-Length", "10")
					_, _ = io.WriteString(rw, "x")
				case "redirect":
					rw.Header().Set("Location", "/again")
					rw.WriteHeader(307)
				}
				return true
			}
			w.mu.Unlock()
			if err = w.s.run(w.ctx, job); !errors.Is(err, errSmartUncertain) {
				t.Fatal(err)
			}
			if err = w.s.run(w.ctx, job); !errors.Is(err, errSmartUncertain) {
				t.Fatal(err)
			}
			w.mu.Lock()
			got := sends
			w.mu.Unlock()
			if got != 1 || w.smartEntry().Smart.Uncertain != 1 {
				t.Fatal("unsafe replay", got, w.smartEntry().Smart)
			}
		})
	}
}

func TestAccSmartOlderRefreshCannotReplaceNewerObservation(t *testing.T) {
	w := smartFixture(t)
	result := w.smartRead()
	entry := w.smartEntry()
	key, err := w.s.enqueue(w.ctx, w.smartRequest(), w.permit, entry.Token.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := w.db.WorkQueue().Get(w.ctx, w.s.kind, key)
	if err != nil {
		t.Fatal(err)
	}
	var job smartJob
	if err = json.Unmarshal(queued.Payload, &job); err != nil {
		t.Fatal(err)
	}
	// The peer reply is delayed while a newer representation publishes its state.
	w.mu.Lock()
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if !r.URL.Query().Has("properties") {
			return false
		}
		w.clock.Advance(time.Second)
		e := w.db.Tx(w.ctx, func(tx context.Context) error {
			current, e := w.db.RemoteCache().Read(tx, entry.Token.Key)
			if e != nil {
				return e
			}
			current.Smart.PropertiesAt = w.clock.Now()
			current.Smart.OwnedProperties = types.Properties{"color": {"newer"}}
			if e = w.p.Items.Annotate(
				tx,
				w.c,
				*result.Item,
				types.Properties{"color": {"newer"}},
				nil,
			); e != nil {
				return e
			}
			_, e = w.db.RemoteCache().Put(tx, current.Token, current)
			return e
		})
		if e != nil {
			t.Error(e)
		}
		_, _ = io.WriteString(rw, `{"properties":{"color":["older"]}}`)
		return true
	}
	w.mu.Unlock()
	if err = w.s.run(w.ctx, queued); err != nil {
		t.Fatal(err)
	}
	props, err := w.db.Items().Properties(w.ctx, result.Item.ID)
	if err != nil || strings.Join(props["color"], ",") != "newer" {
		t.Fatal("older reply overwrote state", props, err)
	}
}

func TestAccSmartDiscoveryThrottlePersistsAcrossCalls(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		rw.Header().Set("Retry-After", "120")
		rw.WriteHeader(429)
		return true
	}
	if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("throttle ignored")
	}
	w.mu.Lock()
	before := w.calls
	w.mu.Unlock()
	if _, err := w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("cooldown ignored")
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if after != before {
		t.Fatal("throttle retried", before, after)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || !state.RetryAfterUntil.Equal(w.clock.Now().Add(120*time.Second)) {
		t.Fatal(state, err)
	}
}

func TestAccSmartNoStoreLeavesNoSynchronizationState(t *testing.T) {
	w := smartFixture(t)
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/artifactory/source/file.bin" {
			return false
		}
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(rw, "artifact body")
		return true
	}
	result, err := w.service.Read(w.ctx, w.smartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if w.bytes(result) != "artifact body" {
		t.Fatal("transient content")
	}
	if _, err = w.db.Items().
		Get(w.ctx, w.c.Key, true, "file.bin"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("private bytes persisted", err)
	}
	stats, err := w.db.WorkQueue().Stats(w.ctx)
	if err != nil || len(stats) != 0 {
		t.Fatal("transient sync jobs", stats, err)
	}
}

func TestAccSmartNegotiationDoesNotSurviveCredentialRevision(t *testing.T) {
	w := smartFixture(t)
	w.smartRead()
	before, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || before.Smart == nil {
		t.Fatal(before, err)
	}
	w.c.Password = "new-source-credential"
	w.saveConfig()
	if _, err = w.service.Read(w.ctx, w.smartRequest()); err == nil {
		t.Fatal("cached negotiation accepted changed peer credential")
	}
	w.mu.Lock()
	denied := w.unauthorized
	w.mu.Unlock()
	if denied != 1 {
		t.Fatal("new credential was not checked by discovery", denied)
	}
	state, err := w.db.RemoteCache().State(w.ctx, w.c.Key)
	if err != nil || state.Smart != nil {
		t.Fatal("stale negotiation persisted", state, err)
	}
}

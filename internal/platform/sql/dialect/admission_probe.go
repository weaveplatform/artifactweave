package dialect

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Temporary [DEBUG-1974] instrumentation; remove before this draft is ready.
var (
	transactionProbeIDs    atomic.Uint64
	transactionProbeOwners sync.Map
)

type transactionProbe struct {
	ctx                                                 context.Context
	db                                                  *sql.DB
	id                                                  uint64
	caller                                              string
	started, acquired, business, finalizers, committing time.Time
	stats                                               sql.DBStats
}

func newTransactionProbe(ctx context.Context, db *sql.DB, name string) *transactionProbe {
	if name != "sqlite" || os.Getenv("ARTIFACTWEAVE_ADMISSION_PROBE") != "1" {
		return nil
	}
	pcs := make([]uintptr, 12)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	names := []string{}
	for {
		frame, more := frames.Next()
		names = append(names, frame.Function)
		if !more {
			break
		}
	}
	return &transactionProbe{ctx: ctx, db: db, id: transactionProbeIDs.Add(1), caller: strings.Join(names, "|"), started: time.Now(), stats: db.Stats()}
}

func (p *transactionProbe) began(err error) {
	if p == nil {
		return
	}
	p.acquired = time.Now()
	if err != nil {
		p.report("begin-error", err)
		return
	}
	transactionProbeOwners.Store(p.db, p)
}

func (p *transactionProbe) phase(name string) {
	if p == nil {
		return
	}
	switch name {
	case "business":
		p.business = time.Now()
	case "finalizers":
		p.finalizers = time.Now()
	case "commit":
		p.committing = time.Now()
	}
}

func (p *transactionProbe) closed(stage string) {
	if p == nil {
		return
	}
	transactionProbeOwners.CompareAndDelete(p.db, p)
	if p.acquired.Sub(p.started) < 100*time.Millisecond && time.Since(p.acquired) < 100*time.Millisecond {
		return
	}
	p.report(stage, nil)
}

func (p *transactionProbe) report(stage string, err error) {
	now := time.Now()
	wait := p.acquired.Sub(p.started)
	hold := now.Sub(p.acquired)
	if stage == "begin-error" {
		hold = 0
	}
	stats := p.db.Stats()
	left := int64(-1)
	if deadline, ok := p.ctx.Deadline(); ok {
		left = time.Until(deadline).Milliseconds()
	}
	ownerID := uint64(0)
	ownerAge := int64(0)
	ownerCaller := ""
	if value, ok := transactionProbeOwners.Load(p.db); ok {
		owner := value.(*transactionProbe)
		ownerID = owner.id
		ownerAge = now.Sub(owner.acquired).Milliseconds()
		ownerCaller = owner.caller
	}
	business := int64(0)
	finalizers := int64(0)
	commit := int64(0)
	if !p.business.IsZero() {
		business = p.business.Sub(p.acquired).Milliseconds()
	}
	if !p.finalizers.IsZero() {
		finalizers = p.finalizers.Sub(p.business).Milliseconds()
	}
	if !p.committing.IsZero() {
		commit = now.Sub(p.committing).Milliseconds()
	}
	slog.Warn("[DEBUG-1974] transaction", "pid", os.Getpid(), "db", fmt.Sprintf("%p", p.db), "id", p.id, "stage", stage,
		"wait_ms", wait.Milliseconds(), "hold_ms", hold.Milliseconds(), "business_ms", business, "finalizers_ms", finalizers, "commit_ms", commit,
		"pool_wait_count", stats.WaitCount-p.stats.WaitCount, "pool_wait_ms", (stats.WaitDuration - p.stats.WaitDuration).Milliseconds(),
		"open", stats.OpenConnections, "in_use", stats.InUse, "max_open", stats.MaxOpenConnections, "deadline_left_ms", left, "ctx_error", p.ctx.Err(), "error", err,
		"owner_id", ownerID, "owner_age_ms", ownerAge, "owner_caller", ownerCaller, "caller", p.caller)
}

package quota

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// WarningKind is the work queue kind of a warning occurrence. Each occurrence
// has its own key, so a retry resends the same message and a rearmed
// threshold queues a new one.
const WarningKind = "configuration.storage-quota-warning"

// state is the durable warning state, one occurrence key per level that is
// currently armed off. It lives in the settings table, so a restart sees the
// occurrences already raised and queues nothing again for them.
type state struct {
	Warning string `json:"warning,omitempty"`
	Limit   string `json:"limit,omitempty"`
}

// warning is the queued occurrence: the immutable facts of the transition
// that raised it, which the message reports.
type warning struct {
	Level          string `json:"level"`
	Key            string `json:"key"`
	LimitPercent   int    `json:"limit_percent"`
	WarningPercent int    `json:"warning_percent"`
	Total          int64  `json:"total"`
	Used           int64  `json:"used"`
}

func (w warning) queueKey() string { return w.Level + "/" + w.Key }

// Admission applies the global storage quota. Its stores join the caller's
// transaction; Capacity is nil when the hot blob store's volume cannot be
// measured, as for object storage.
type Admission struct {
	Configs  configdoc.ConfigStore
	Settings settings.SettingsStore
	Queue    work.WorkQueue
	Capacity configapi.StorageCapacity
	// Now stamps queued occurrences; nil is time.Now.
	Now func() time.Time
}

// Admit decides a transaction's net positive storage growth, inside that
// transaction and after the composition's accounting guard, which serializes
// every admission. The measurement includes the blob bytes racing uploads have
// already written, so a writer admitted after another sees it: no ordering of
// concurrent uploads passes the limit. Growth of zero or less, reads and
// deletions are never refused, so space can always be reclaimed. A warning
// transition is recorded in the same transaction and commits only with the
// write that observed it.
func (a Admission) Admit(ctx context.Context, delta int64) error {
	if delta <= 0 {
		return nil
	}
	p, _, err := Read(ctx, a.Configs)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return nil
	}
	total, used, err := a.measure(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", kernel.ErrStorageCapacity, err)
	}
	level := p.Level(total, used)
	if level == Limit {
		// The usage stays out of the answer, as 7.x keeps it out of the
		// client's message; the monitor's occurrence carries it to admins.
		return kernel.ErrDiskQuota
	}
	return a.record(ctx, p, total, used, level)
}

// Check reconciles the warning state with a fresh measurement in the caller's
// transaction: the monitor's way of noticing a volume that filled or emptied
// without a write here, and of raising the limit occurrence, which a refused
// write rolls back. A disabled policy clears the state, so enabling it again
// warns afresh. A failed measurement changes nothing.
func (a Admission) Check(ctx context.Context) error {
	p, _, err := Read(ctx, a.Configs)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return a.save(ctx, state{})
	}
	total, used, err := a.measure(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", kernel.ErrStorageCapacity, err)
	}
	return a.record(ctx, p, total, used, p.Level(total, used))
}

func (a Admission) measure(ctx context.Context) (int64, int64, error) {
	if a.Capacity == nil {
		return 0, 0, fmt.Errorf("no measurable blob volume: %w", kernel.ErrStore)
	}
	total, used, err := a.Capacity.Measure(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("measure: %w", err)
	}
	if total <= 0 || used < 0 || used > total {
		return 0, 0, fmt.Errorf("implausible blob volume measurement: %w", kernel.ErrStore)
	}
	return total, used, nil
}

// record moves the state to level. Rising past a threshold that is not armed
// off raises one occurrence; falling below it rearms it. Reaching the limit
// also arms off the warning, so falling back from the limit to the warning
// band sends nothing new, while rising to the limit again does.
func (a Admission) record(ctx context.Context, p Policy, total, used int64, level Level) error {
	current, err := readState(ctx, a.Settings)
	if err != nil {
		return err
	}
	next := current
	var raise []warning
	occurrence := func(l Level) warning {
		return warning{
			Level: l.String(), Key: rand.Text(), LimitPercent: p.LimitPercent, WarningPercent: p.Warning,
			Total: total, Used: used,
		}
	}
	switch level {
	case Below:
		next = state{}
	case Warning:
		next.Limit = ""
		if next.Warning == "" {
			w := occurrence(Warning)
			next.Warning = w.Key
			raise = append(raise, w)
		}
	case Limit:
		if next.Limit == "" {
			w := occurrence(Limit)
			next.Limit = w.Key
			raise = append(raise, w)
		}
		if next.Warning == "" {
			next.Warning = rand.Text()
		}
	}
	if next == current {
		return nil
	}
	if err := a.save(ctx, next); err != nil {
		return err
	}
	for _, w := range raise {
		if err := enqueue(ctx, a.Queue, w, a.now()); err != nil {
			return err
		}
	}
	return nil
}

func (a Admission) save(ctx context.Context, next state) error {
	current, err := readState(ctx, a.Settings)
	if err != nil {
		return err
	}
	if current == next {
		return nil
	}
	if next == (state{}) {
		if err := a.Settings.Delete(ctx, settings.SettingStorageQuotaWarning); err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("rearm storage quota warnings: %w", err)
		}
		return nil
	}
	if err := a.Settings.Put(ctx, settings.SettingStorageQuotaWarning, next); err != nil {
		return fmt.Errorf("save storage quota warning state: %w", err)
	}
	return nil
}

func readState(ctx context.Context, s settings.SettingsStore) (state, error) {
	var st state
	err := s.Get(ctx, settings.SettingStorageQuotaWarning, &st)
	if errors.Is(err, kernel.ErrNotFound) {
		return state{}, nil
	}
	if err != nil {
		return state{}, fmt.Errorf("read storage quota warning state: %w", err)
	}
	return st, nil
}

func enqueue(ctx context.Context, queue work.WorkQueue, w warning, now time.Time) error {
	if queue == nil {
		return fmt.Errorf("storage quota warning handoff unavailable: %w", kernel.ErrStore)
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("encode storage quota warning: %w", err)
	}
	if err := queue.Enqueue(ctx, WarningKind, w.queueKey(), payload, now); err != nil {
		return fmt.Errorf("queue storage quota warning: %w", err)
	}
	return nil
}

func (a Admission) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Monitor runs Check on its own guarded transaction.
type Monitor struct {
	Admission Admission
	Tx        tx.Runner
	// Guard is the shared configuration/accounting guard that serializes
	// Check with every admission.
	Guard func(context.Context) error
	// Log receives failed checks; nil discards them.
	Log *slog.Logger
}

// Check measures once in a new transaction.
func (m Monitor) Check(ctx context.Context) error {
	if m.Tx == nil || m.Guard == nil {
		return fmt.Errorf("storage quota monitor unavailable: %w", kernel.ErrStore)
	}
	err := m.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := m.Guard(ctx); err != nil {
			return fmt.Errorf("guard storage quota check: %w", err)
		}
		return m.Admission.Check(ctx)
	})
	if err != nil {
		return fmt.Errorf("storage quota check: %w", err)
	}
	return nil
}

// Run checks at once and then every interval until ctx ends. A failed check
// is logged and retried on the next tick; it never stops the loop.
func (m Monitor) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := m.Check(ctx); err != nil && ctx.Err() == nil && m.Log != nil {
			m.Log.Warn("storage quota check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

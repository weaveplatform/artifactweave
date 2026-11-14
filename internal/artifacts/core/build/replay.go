package build

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// AppendKeyed is a keyed append's effect, which the replay runs inside its
// transaction (decision 0043, "Optional protected build append"). It locks
// the run the request selects before anything else, so the receipt binds the
// append to that run even when a newer run of the same number appears later,
// appends to it with the access current gives, which the caller evaluates
// from the credential as it stands now rather than the evaluator cached at
// login, and returns the run's ID for the receipt. Only metadata mutations
// and the existing immutable document-blob write run here; no remote work
// is dispatched from the transaction.
func (s *Service) AppendKeyed(
	ctx context.Context,
	ap Append,
	current func(context.Context) (Access, error),
) (int64, error) {
	if current == nil {
		return 0, ErrForbidden
	}
	b, err := s.Store.Builds().
		GetForUpdate(ctx, ProjectKey(ap.ProjectKey), ap.Name, ap.Number, kernel.Normalise(ap.Started))
	if err != nil {
		return 0, fmt.Errorf("build: select append target: %w", err)
	}
	a, err := current(ctx)
	if err != nil {
		return 0, err
	}
	selected := ap
	selected.Started = b.Started
	if err := s.Append(ctx, selected, a); err != nil {
		return 0, err
	}
	return b.ID, nil
}

// ConfirmAppend is a keyed append's reload: it locks the run id names, which
// the receipt holds, and confirms that it is still the run the request
// addresses and that the credential, as current gives it now, may still
// append to it. A run deleted since answers ErrNotFound, one renamed or
// moved ErrConflict, and revoked access ErrForbidden, so that a replay never
// outlives the access that made it.
func (s *Service) ConfirmAppend(
	ctx context.Context,
	ap Append,
	id int64,
	current func(context.Context) (Access, error),
) error {
	if current == nil {
		return ErrForbidden
	}
	started := kernel.Normalise(ap.Started)
	b, err := s.Store.Builds().ByIDForUpdate(ctx, id)
	if err != nil {
		return fmt.Errorf("build: original append target: %w", err)
	}
	if b.ProjectKey != ProjectKey(ap.ProjectKey) || b.Name != ap.Name ||
		b.Number != ap.Number ||
		!started.IsZero() && !b.Started.Equal(started) {
		return fmt.Errorf("build: original append target changed: %w", ErrConflict)
	}
	c, err := s.repository(ctx, b.ProjectKey)
	if err != nil {
		return err
	}
	a, err := current(ctx)
	if err != nil {
		return err
	}
	_, path := b.DocumentItem()
	if !a.Build(c.Key, path, false, api.ActionWrite|api.ActionDelete) {
		return ErrForbidden
	}
	return nil
}

// The actions a build report records.
const (
	actionDelete  = "delete"
	actionPromote = "promote"
)

// pruneReports bounds how many expired reports one keyed action removes
// before it stores its own, so that expired reports never pile up and no
// action pays for more than a small, fixed cleanup.
const pruneReports = 100

// deletedRuns is a keyed delete's report: the runs it removed, in the order
// it removed them, which its answer lists.
type deletedRuns struct {
	Runs []deletedRun `json:"runs"`
}

type deletedRun struct {
	Number  string    `json:"number"`
	Started time.Time `json:"started"`
}

// promotionMessages is a keyed promotion's report: its messages, which its
// answer lists and which the files it moved no longer let a replay rebuild.
type promotionMessages struct {
	Messages []promotionMessage `json:"messages"`
}

type promotionMessage struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// DeleteKeyed is a keyed delete's effect, which the replay runs inside its
// transaction (decision 0043): Delete, with the access current gives now,
// which also stores the deleted runs as a report kept for retain, in the
// delete's transaction. A replay answers from that report, since the runs
// are gone. It returns the deleted runs and the report's ID for the
// receipt.
func (s *Service) DeleteKeyed(
	ctx context.Context,
	d Delete,
	current func(context.Context) (Access, error),
	retain time.Duration,
) ([]model.BuildRun, int64, error) {
	if current == nil {
		return nil, 0, ErrForbidden
	}
	a, err := current(ctx)
	if err != nil {
		return nil, 0, err
	}
	var id int64
	gone, err := s.deleteThen(ctx, d, a, func(ctx context.Context, gone []model.BuildRun) error {
		report := deletedRuns{Runs: make([]deletedRun, 0, len(gone))}
		for _, r := range gone {
			report.Runs = append(report.Runs, deletedRun{Number: r.Number, Started: r.Started})
		}
		var err error
		id, err = s.putReport(
			ctx,
			actionDelete,
			ProjectKey(d.ProjectKey),
			d.Name,
			0,
			report,
			retain,
		)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return gone, id, nil
}

// ConfirmDelete is a keyed delete's reload: it locks the report id names,
// which the receipt holds, confirms that it is a delete of the build d
// names, and that the credential, as current gives it now, may still
// delete each run it lists, and returns those runs. A report of another
// action or build answers ErrConflict, and revoked access ErrForbidden.
func (s *Service) ConfirmDelete(
	ctx context.Context,
	d Delete,
	id int64,
	current func(context.Context) (Access, error),
) ([]model.BuildRun, error) {
	if current == nil {
		return nil, ErrForbidden
	}
	r, err := s.report(ctx, id, actionDelete, ProjectKey(d.ProjectKey), d.Name)
	if err != nil {
		return nil, err
	}
	var report deletedRuns
	if err := json.Unmarshal(r.Report, &report); err != nil {
		return nil, fmt.Errorf("build: read delete report: %w", err)
	}
	c, err := s.repository(ctx, r.ProjectKey)
	if err != nil {
		return nil, err
	}
	a, err := current(ctx)
	if err != nil {
		return nil, err
	}
	gone := make([]model.BuildRun, 0, len(report.Runs))
	for _, run := range report.Runs {
		g := model.BuildRun{
			ProjectKey: r.ProjectKey, Name: r.Name, Number: run.Number, Started: run.Started,
		}
		if !a.Build(c.Key, documentPath(g), false, api.ActionDelete) {
			return nil, ErrForbidden
		}
		gone = append(gone, g)
	}
	return gone, nil
}

// PromoteKeyed is a keyed promotion's effect, which the replay runs inside
// its transaction (decision 0043): Promote, with the access current gives
// now, which also stores the promotion's messages as a report kept for
// retain, in the promotion's transaction. The report's ID, for the
// receipt, is zero for a dry run or an aborted promotion, which changed
// nothing and so leaves nothing to replay.
func (s *Service) PromoteKeyed(
	ctx context.Context,
	p Promotion,
	current func(context.Context) (Access, error),
	retain time.Duration,
) (PromotionResult, int64, error) {
	if current == nil {
		return PromotionResult{}, 0, ErrForbidden
	}
	a, err := current(ctx)
	if err != nil {
		return PromotionResult{}, 0, err
	}
	var id int64
	res, err := s.promoteThen(
		ctx,
		p,
		a,
		func(ctx context.Context, run int64, msgs []item.Message) error {
			report := promotionMessages{Messages: make([]promotionMessage, 0, len(msgs))}
			for _, m := range msgs {
				report.Messages = append(report.Messages, promotionMessage(m))
			}
			var err error
			id, err = s.putReport(
				ctx,
				actionPromote,
				ProjectKey(p.ProjectKey),
				p.Name,
				run,
				report,
				retain,
			)
			return err
		},
	)
	if err != nil {
		return PromotionResult{}, 0, err
	}
	return res, id, nil
}

// ConfirmPromote is a keyed promotion's reload: it locks the report id
// names, which the receipt holds, and the run it promoted, confirms that
// the run is still the build and number p addresses and that the
// credential, as current gives it now, may still promote it, and returns
// the promotion's messages. A run deleted since answers ErrNotFound, one
// renamed ErrConflict, and revoked access ErrForbidden.
func (s *Service) ConfirmPromote(
	ctx context.Context,
	p Promotion,
	id int64,
	current func(context.Context) (Access, error),
) ([]item.Message, error) {
	if current == nil {
		return nil, ErrForbidden
	}
	projectKey := ProjectKey(p.ProjectKey)
	r, err := s.report(ctx, id, actionPromote, projectKey, p.Name)
	if err != nil {
		return nil, err
	}
	b, err := s.Store.Builds().ByIDForUpdate(ctx, r.BuildID)
	if err != nil {
		return nil, fmt.Errorf("build: promoted run: %w", err)
	}
	if b.ProjectKey != projectKey || b.Name != p.Name || b.Number != p.Number {
		return nil, fmt.Errorf("build: promoted run changed: %w", ErrConflict)
	}
	var report promotionMessages
	if err := json.Unmarshal(r.Report, &report); err != nil {
		return nil, fmt.Errorf("build: read promotion report: %w", err)
	}
	a, err := current(ctx)
	if err != nil {
		return nil, err
	}
	repoKey, path := b.DocumentItem()
	if !a.Build(repoKey, path, false, api.ActionWrite) {
		return nil, ErrForbidden
	}
	msgs := make([]item.Message, 0, len(report.Messages))
	for _, m := range report.Messages {
		msgs = append(msgs, item.Message(m))
	}
	return msgs, nil
}

// putReport removes a few expired reports and stores a keyed action's
// report, kept for retain, and returns its ID.
func (s *Service) putReport(
	ctx context.Context,
	action, projectKey, name string,
	run int64,
	report any,
	retain time.Duration,
) (int64, error) {
	encoded, err := json.Marshal(report)
	if err != nil {
		return 0, fmt.Errorf("build: encode %s report: %w", action, err)
	}
	now := s.now()
	if _, err := s.Store.Builds().PruneReports(ctx, now, pruneReports); err != nil {
		return 0, fmt.Errorf("build: prune reports: %w", err)
	}
	id, err := s.Store.Builds().PutReport(ctx, model.BuildReport{
		Action: action, ProjectKey: projectKey, Name: name, BuildID: run,
		Report: encoded, Created: now, Expires: now.Add(retain),
	})
	if err != nil {
		return 0, fmt.Errorf("build: store %s report: %w", action, err)
	}
	return id, nil
}

// report locks the report with id and confirms it is action's on the build
// projectKey and name address; a receipt only names its own action's
// report, so another is ErrConflict.
func (s *Service) report(
	ctx context.Context,
	id int64,
	action, projectKey, name string,
) (model.BuildReport, error) {
	r, err := s.Store.Builds().ReportForUpdate(ctx, id)
	if err != nil {
		return r, fmt.Errorf("build: %s report: %w", action, err)
	}
	if r.Action != action || r.ProjectKey != projectKey || r.Name != name {
		return r, fmt.Errorf("build: %s report changed: %w", action, ErrConflict)
	}
	return r, nil
}

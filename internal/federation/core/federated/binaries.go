package federated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	streamsync "github.com/weaveplatform/artifactweave/internal/federation/core/sync"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

// BinaryStore reads failed fetches and the pending changes they supply.
type BinaryStore interface {
	tx.Runner
	Outbox() outbox.OutboxStore
	Stamps() port.ItemStampStore
}

// FailedBinary is one failed content fetch, shared by every pending path
// with the same digest in one repository. Path names the first pending path.
type FailedBinary struct {
	SHA256   string
	SHA1     string
	RepoKey  string
	Path     string
	Created  time.Time
	Failures int
}

// BinaryCounts is the inferred binary-task DTO's failed content count.
type BinaryCounts struct{ FailedTasks int }

// Binaries supplies FED-06's operator controls. Reads and deletions run under
// current authority in one transaction. Replay queues fresh work atomically;
// workers fetch outside metadata transactions. The stream engine publishes the
// still-current pending change in the transaction that claims its failed task
// and claims its failure record, so an operator deletion cancels a retry.
type Binaries struct {
	Store        BinaryStore
	Repositories interface {
		Get(context.Context, string) (repo.Config, error)
	}
	Authorize func(context.Context) error
	Read      func(context.Context, string) error
	Streams   func() *streamsync.Engine
}

var sha1Hex = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (b Binaries) repository(ctx context.Context, key string, read bool) error {
	if read {
		if err := b.Read(ctx, key); errors.Is(err, kernel.ErrNotFound) {
			return ErrNoRepository
		} else if err != nil {
			return err
		}
	} else if err := b.Authorize(ctx); err != nil {
		return err
	}
	c, err := b.Repositories.Get(ctx, key)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return ErrNoRepository
	case err != nil:
		return fmt.Errorf("read repository %s: %w", key, err)
	case c.Class != repo.Federated:
		return refuse("Repository '%s' is not a federated repository", key)
	}
	return nil
}

// List returns one page in SHA-256 order, and whether another page remains.
// Page sizes and page numbers must be positive.
func (b Binaries) List(ctx context.Context, key string, limit, page int) ([]FailedBinary, bool, error) {
	var out []FailedBinary
	var more bool
	err := b.Store.Tx(ctx, func(ctx context.Context) error {
		if err := b.repository(ctx, key, false); err != nil {
			return err
		}
		if limit < 1 || page < 1 {
			return refuse("limit and page must be at least 1")
		}
		all, err := b.tasks(ctx, key, "")
		if err != nil {
			return err
		}
		out = []FailedBinary{}
		// Check the quotient before multiplying: a huge page cannot overflow.
		if len(all) == 0 || page-1 > (len(all)-1)/limit {
			return nil
		}
		start := (page - 1) * limit
		end := start + min(limit, len(all)-start)
		out, more = all[start:end], end < len(all)
		return nil
	})
	return out, more, err //nolint:wrapcheck // the callback names its failures
}

func (b Binaries) tasks(ctx context.Context, key, sha1 string) ([]FailedBinary, error) {
	fs, err := b.Store.Outbox().FailedFetches(ctx, model.RepositoryStream(key))
	if err != nil {
		return nil, fmt.Errorf("failed binaries of %s: %w", key, err)
	}
	out := make([]FailedBinary, 0, len(fs))
	for _, f := range fs {
		w, err := b.Store.Stamps().PendingOn(ctx, key, f.SHA256)
		if err != nil {
			return nil, fmt.Errorf("pending binaries of %s: %w", key, err)
		}
		task := FailedBinary{SHA256: f.SHA256, SHA1: f.SHA1, RepoKey: key, Created: f.FirstFailed, Failures: f.Failures}
		if len(w) > 0 {
			task.Path = w[0].Path
			var p payload
			if err := json.Unmarshal(w[0].Pending, &p); err != nil {
				return nil, fmt.Errorf("pending binary %s:%s: %w", key, task.Path, err)
			}
			if task.SHA1 == "" {
				task.SHA1 = p.Actual.SHA1
			}
		}
		if sha1 == "" || strings.EqualFold(task.SHA1, sha1) {
			out = append(out, task)
		}
	}
	return out, nil
}

func checkSHA1(sha1 string) error {
	if sha1 != "" && !sha1Hex.MatchString(sha1) {
		return refuse("sha1 must be 40 hexadecimal digits")
	}
	return nil
}

// Delete removes matching failed tasks. Pending changes remain absent until a
// newer entry or full sync supplies them; a queued retry finds no task.
func (b Binaries) Delete(ctx context.Context, key, sha1 string) (int, error) {
	n := 0
	err := b.Store.Tx(ctx, func(ctx context.Context) error {
		if err := b.repository(ctx, key, false); err != nil {
			return err
		}
		if err := checkSHA1(sha1); err != nil {
			return err
		}
		tasks, err := b.tasks(ctx, key, sha1)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if err := b.Store.Outbox().ClearFailedFetch(ctx, model.RepositoryStream(key), task.SHA256); errors.Is(err, kernel.ErrNotFound) {
				continue
			} else if err != nil {
				return fmt.Errorf("delete failed binary: %w", err)
			}
			n++
		}
		return nil
	})
	return n, err //nolint:wrapcheck // the callback names its failures
}

// Replay marks every matching failed task for immediate durable retry, resetting
// its attempts and overriding backoff in the current-authority transaction.
// Workers fetch outside that transaction; the response counts tasks marked.
func (b Binaries) Replay(ctx context.Context, key, sha1 string) (int, error) {
	n := 0
	err := b.Store.Tx(ctx, func(ctx context.Context) error {
		if err := b.repository(ctx, key, false); err != nil {
			return err
		}
		if err := checkSHA1(sha1); err != nil {
			return err
		}
		tasks, err := b.tasks(ctx, key, sha1)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		if b.Streams == nil {
			return ErrNoStreams
		}
		e := b.Streams()
		if e == nil {
			return ErrNoStreams
		}
		for _, task := range tasks {
			if err := b.Store.Outbox().ResetFailedFetch(ctx, model.RepositoryStream(key), task.SHA256); errors.Is(err, kernel.ErrNotFound) {
				continue
			} else if err != nil {
				return fmt.Errorf("reset failed binary: %w", err)
			}
			if err := e.QueueFetch(ctx, model.RepositoryStream(key), task.SHA256); err != nil {
				return fmt.Errorf("replay failed binary of %s: %w", key, err)
			}
			n++
		}
		return nil
	})
	return n, err //nolint:wrapcheck // the callback names its failures
}

// Counts is the repository's failed-fetch count. read chooses repository Read
// authority for repoState; the task endpoint requires an administrator.
func (b Binaries) Counts(ctx context.Context, key string, read bool) (BinaryCounts, error) {
	var out BinaryCounts
	err := b.Store.Tx(ctx, func(ctx context.Context) error {
		if err := b.repository(ctx, key, read); err != nil {
			return err
		}
		fs, err := b.Store.Outbox().FailedFetches(ctx, model.RepositoryStream(key))
		if err != nil {
			return fmt.Errorf("failed binaries of %s: %w", key, err)
		}
		out.FailedTasks = len(fs)
		return nil
	})
	return out, err //nolint:wrapcheck // the callback names its failures
}

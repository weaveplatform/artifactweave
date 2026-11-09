package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

// ErrInvalidWorker reports a worker definition or state the runtime refuses.
var ErrInvalidWorker = errors.New("invalid worker")

// WorkerAction is the hook a worker runs on, in JFrog's names
// (BEFORE_DOWNLOAD, AFTER_CREATE, GENERIC_EVENT and so on).
type WorkerAction string

// Before reports whether the action runs synchronously before the operation
// and can refuse it (decision 0026).
func (a WorkerAction) Before() bool { return strings.HasPrefix(string(a), "BEFORE_") }

// Worker limits from decision 0026, matching JFrog's defaults.
const (
	MaxWorkerSource     = 100000
	MaxWorkerStateItems = 10
	MaxWorkerStateKey   = 40
	MaxWorkerStateValue = 250
)

// WorkerFilter restricts an artifact-event worker to repositories and paths.
type WorkerFilter struct {
	RepoKeys        []string `json:"repo_keys,omitempty"`
	IncludePatterns []string `json:"include_patterns,omitempty"`
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`
	AnyLocal        bool     `json:"any_local,omitempty"`
	AnyRemote       bool     `json:"any_remote,omitempty"`
	AnyFederated    bool     `json:"any_federated,omitempty"`
}

// Matches reports whether an event on itemPath in repository repoKey of
// class c selects the worker.
func (f WorkerFilter) Matches(repoKey string, c repo.Class, itemPath string) bool {
	repoOK := slices.Contains(f.RepoKeys, repoKey) ||
		(f.AnyLocal && c == repo.Local) ||
		(f.AnyRemote && c == repo.Remote) ||
		(f.AnyFederated && c == repo.Federated)
	if !repoOK {
		return false
	}
	return PatternFilter{Include: f.IncludePatterns, Exclude: f.ExcludePatterns}.Matches(itemPath)
}

// Worker is a TypeScript hook run by the embedded runtime.
type Worker struct {
	Key          string
	Action       WorkerAction
	SourceCode   string
	Enabled      bool
	Description  string
	Debug        bool
	Shared       bool
	ProjectKey   string
	Filter       WorkerFilter
	ProgramHash  string
	Failures     int
	BlockedUntil time.Time
	Created      time.Time
	Updated      time.Time
}

// Blocked reports whether repeated failures have suspended the worker.
func (w Worker) Blocked(now time.Time) bool { return now.Before(w.BlockedUntil) }

// ValidateWorkerSource applies the size limit checked on save.
func ValidateWorkerSource(src string) error {
	if src == "" || len(src) > MaxWorkerSource {
		return fmt.Errorf(
			"%w: source must be 1 to %d characters",
			ErrInvalidWorker,
			MaxWorkerSource,
		)
	}
	return nil
}

// ValidateWorkerState applies the per-worker state limits; an execution
// whose state breaks them fails and its state is not saved.
func ValidateWorkerState(state map[string]string) error {
	if len(state) > MaxWorkerStateItems {
		return fmt.Errorf(
			"%w: %d state items exceeds %d",
			ErrInvalidWorker,
			len(state),
			MaxWorkerStateItems,
		)
	}
	for k, v := range state {
		if k == "" || utf8.RuneCountInString(k) > MaxWorkerStateKey {
			return fmt.Errorf("%w: state key %q", ErrInvalidWorker, k)
		}
		if utf8.RuneCountInString(v) > MaxWorkerStateValue {
			return fmt.Errorf("%w: state value of %q is too long", ErrInvalidWorker, k)
		}
	}
	return nil
}

// WorkerTrigger is what started an execution.
type WorkerTrigger string

// Worker triggers.
const (
	TriggerEvent WorkerTrigger = "event"
	TriggerHTTP  WorkerTrigger = "http"
	TriggerTest  WorkerTrigger = "test"
	TriggerRerun WorkerTrigger = "rerun"
)

// WorkerExecution is one recorded run of a worker.
type WorkerExecution struct {
	ID        string
	WorkerKey string
	Action    WorkerAction
	Trigger   WorkerTrigger
	Status    string
	Started   time.Time
	Duration  time.Duration
	Payload   []byte
	Result    []byte
	Logs      []byte
	Error     string
}

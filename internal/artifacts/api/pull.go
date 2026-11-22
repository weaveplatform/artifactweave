package api

import (
	"context"
	"errors"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Pull failure categories let another module make recovery decisions without
// importing artifacts' source/transport implementation or retaining raw errors.
var (
	ErrPullChanged     = errors.New("artifacts: selected pull facts changed")
	ErrPullDenied      = errors.New("artifacts: pull authority refused")
	ErrPullOffline     = errors.New("artifacts: pull source is offline")
	ErrPullNotStored   = errors.New("artifacts: pull response was not stored")
	ErrPullIntegrity   = errors.New("artifacts: pull representation failed validation")
	ErrPullUnsupported = errors.New("artifacts: pull operation is unsupported")
	ErrPullTemporary   = errors.New("artifacts: pull source temporarily unavailable")
)

// PullSelection bounds canonical remote-cache paths independently of the
// upstream layout. Empty include selects every path under Root; exclusions win.
type PullSelection struct {
	Root       string `json:"root"`
	Include    string `json:"include"`
	Exclude    string `json:"exclude"`
	Properties bool   `json:"properties"`
}

// PullRepository binds one remote lifetime, layout and selected transport
// revision. Revision is a hash, never raw options or credentials. It conveys no
// current caller, project or network authority.
type PullRepository struct {
	Repository TransferRepository `json:"repository"`
	Revision   string             `json:"revision"`
}

// PullItem is an immutable, mapped upstream snapshot. Version binds computed
// bytes and selected metadata without introducing an alternative cache identity.
// UpstreamPath is descriptive data, never a URL or a request destination.
type PullItem struct {
	Repository   PullRepository      `json:"repository"`
	Selection    PullSelection       `json:"selection"`
	Path         string              `json:"path"`
	UpstreamPath string              `json:"upstream_path"`
	Size         int64               `json:"size"`
	Actual       kernel.Checksums    `json:"actual"`
	Modified     time.Time           `json:"modified"`
	Updated      time.Time           `json:"updated"`
	Properties   map[string][]string `json:"properties"`
	Version      string              `json:"version"`
}

// PullPublication narrows a trusted worker's cache effects to its current
// configuration and queue attempt. Check runs before source/cache work; Fence
// is last in a publication transaction, after all domain and event effects.
type PullPublication struct {
	Check func(context.Context) error
	// Record joins successful cache/property publication before Fence. The
	// direction driver advances its durable selected intent here atomically.
	Record func(context.Context, PullCached) error
	Fence  func(context.Context) error
}

// PullCacheCandidate binds all current representations and format companions
// of one selected canonical cache path. It grants no upstream or Delete right.
type PullCacheCandidate struct {
	Repository PullRepository `json:"repository"`
	Selection  PullSelection  `json:"selection"`
	Path       string         `json:"path"`
	Version    string         `json:"version"`
}

// PullCached identifies an actual P9 representation, including raw protocol
// metadata. The hashes describe a committed cache version, never permission to
// reuse it. No credentials, transport options or private cache token escape.
type PullCached struct {
	Item           PullItem `json:"item"`
	Representation string   `json:"representation"`
	Version        string   `json:"version"`
}

// PullSource publishes bounded selection from an owning remote through P9.
// Every call reloads current source/configuration and caller authority. An empty
// page may still have a continuation; only an empty continuation ends a walk.
// Selection is not a global upstream snapshot and grants no cache mutation.
type PullSource interface {
	Repository(context.Context, string, PullSelection) (PullRepository, error)
	Items(context.Context, PullRepository, PullSelection, kernel.Page) ([]PullItem, string, error)
	// Fetch admits complete selected bytes through the ordinary format/cache
	// service outside transactions. A transient/no-store response is not a
	// successful cache population. Selected properties preserve local edits.
	Fetch(context.Context, PullItem, PullPublication) (PullCached, error)
	// WithCurrent joins a short local transaction, refreshes caller/config and
	// the exact committed representation, then runs checkpoint work. The
	// callback must perform no network I/O and end with its ownership fence.
	WithCurrent(context.Context, PullCached, func(context.Context) error) error
	// CachedItems reads a bounded current cache page without contacting peers.
	CachedItems(context.Context, PullRepository, PullSelection, kernel.Page) ([]PullCacheCandidate, string, error)
	// Remove requires exact upstream absence, current Delete on the normal
	// removal group, and unchanged cache facts. Record receives actual paths
	// after cache/item/index/event effects, before the final execution fence.
	// If the upstream still has this file, Record receives no paths in a
	// transaction after current source/cache and selected Delete checks, before Fence. That
	// observation acknowledges retention, never successful deletion. It cannot
	// turn an empty removal receipt into acknowledged removal evidence.
	Remove(context.Context, PullCacheCandidate, PullPublication, func(context.Context, []string) error) ([]string, error)
}

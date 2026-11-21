package api

import (
	"context"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
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

// PullSource publishes bounded selection from an owning remote through P9.
// Every call reloads current source/configuration and caller authority. An empty
// page may still have a continuation; only an empty continuation ends a walk.
// Selection is not a global upstream snapshot and grants no cache mutation.
type PullSource interface {
	Repository(context.Context, string, PullSelection) (PullRepository, error)
	Items(context.Context, PullRepository, PullSelection, kernel.Page) ([]PullItem, string, error)
}

package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// LegacyRoots are the root-relative directories that hold the pre-migration
// packages. Every package directory at or below one of them needs a package
// entry. pkg/client and pkg/containertest keep their paths, so they are not
// legacy. A root stays listed after its own package has moved, until
// promotion, so pkgmove neither rewrites nor checks this file: a stale path
// written anywhere else in it goes unreported.
var LegacyRoots = []string{
	"internal",
	"pkg/aql",
	"pkg/blob",
	"pkg/cdn",
	"pkg/layout",
	"pkg/repo",
	"pkg/store",
	"pkg/types",
}

// ErrInvalid is wrapped by every error that reports a ledger breaking its
// rules, from Validate and from Load's check for content after the object.
var ErrInvalid = errors.New("invalid ledger")

// Actions of package and file entries.
const (
	// ActionMove moves a legacy directory, or a file, to a target that no
	// other package entry uses.
	ActionMove = "move"
	// ActionMerge moves a legacy directory into a target that at least one
	// other merge entry shares. Package entries only.
	ActionMerge = "merge"
	// ActionSplit sends the contents of a legacy directory, or the
	// declarations of a file, to several targets. A split package has no
	// single target; the tickets that move its files out add file entries.
	ActionSplit = "split"
	// ActionDelete removes a legacy directory or file; whatever replaces it
	// lives elsewhere.
	ActionDelete = "delete"
)

// Target scopes.
const (
	// ScopeRoot, the default when scope is empty, reads a target relative to
	// the staging root, so the entry stays true after promotion renames the
	// staging root to the final root.
	ScopeRoot = "root"
	// ScopeRepo reads a target relative to the repository root. It must start
	// with test/; it is how a legacy test file reaches a different target
	// than its package.
	ScopeRepo = "repo"
)

// Statuses that Ledger.Status derives from the tree. Package and file entries
// take pending, bridged, done or broken; placeholders take ok, missing or
// implemented.
const (
	// StatusPending means the legacy code is still in place.
	StatusPending = "pending"
	// StatusBridged means the legacy directory holds only type-alias bridge
	// files (bridge.go, bridge_*.go) and, for a move or merge, the target
	// holds Go files.
	StatusBridged = "bridged"
	// StatusDone means the legacy code is gone (the legacy directory holds no
	// Go files, or the legacy file is absent) and, for a move or merge, the
	// target holds Go files, or for a file move, the target exists.
	StatusDone = "done"
	// StatusBroken means the legacy code is gone but the target of its move
	// or merge holds no Go files, or the target of its file move is absent.
	StatusBroken = "broken"
	// StatusOK means a placeholder directory holds doc.go and no other Go
	// file.
	StatusOK = "ok"
	// StatusMissing means a placeholder directory holds no doc.go: it is
	// absent or has no Go files.
	StatusMissing = "missing"
	// StatusImplemented means a placeholder directory holds a Go file other
	// than doc.go, so its entry must go.
	StatusImplemented = "implemented"
)

// Ledger is the content of migration/ledger.json.
type Ledger struct {
	// Schema is the version of the file format, 1.
	Schema int `json:"schema"`
	// Module is the Go module path.
	Module string `json:"module"`
	// StagingRoot is the directory that holds the staged packages, migration.
	StagingRoot string `json:"staging_root"`
	// FinalRoot is the directory the staging root becomes, internal.
	FinalRoot string `json:"final_root"`
	// Packages has one entry per legacy package directory.
	Packages []Package `json:"packages"`
	// Files has entries for files that leave a legacy directory on their own.
	Files []File `json:"files"`
	// Placeholders lists the packages that exist with doc.go only until the
	// phase that implements them.
	Placeholders []Placeholder `json:"placeholders"`
}

// Package maps one legacy package directory to its target.
type Package struct {
	// From is the legacy directory, slash-separated and relative to the
	// repository root.
	From string `json:"from"`
	// Action is ActionMove, ActionMerge, ActionSplit or ActionDelete.
	Action string `json:"action"`
	// To is the target directory of a move or merge, relative to the staging
	// root, or to the repository root under ScopeRepo.
	To string `json:"to,omitempty"`
	// Scope is ScopeRoot (when empty) or ScopeRepo.
	Scope string `json:"scope,omitempty"`
	// Name is the target package name where it is not the last element of
	// To, as for the <type>/http targets, whose packages are named
	// <type>http so that they do not shadow net/http.
	Name string `json:"name,omitempty"`
	// Rename maps a file name in From to its name in the target.
	Rename map[string]string `json:"rename,omitempty"`
	// Ticket is the backlog ID of the ticket that owns the entry, such as
	// "ECO-06".
	Ticket string `json:"ticket"`
	// Note is free text for reviewers.
	Note string `json:"note,omitempty"`
}

// File maps one file, or an asset directory written with a trailing "/", that
// leaves a legacy directory on its own: a file of a split package, or an
// exception that does not travel with its moving package.
type File struct {
	// From is the legacy file or asset directory, relative to the repository
	// root.
	From string `json:"from"`
	// Action is ActionMove, ActionSplit or ActionDelete.
	Action string `json:"action"`
	// To is the target directory of a move, read as Package.To is.
	To string `json:"to,omitempty"`
	// Scope is ScopeRoot (when empty) or ScopeRepo.
	Scope string `json:"scope,omitempty"`
	// File is the name in To where it differs from the last element of
	// From.
	File string `json:"file,omitempty"`
	// Targets are the directories a split file's declarations go to; at
	// least two.
	Targets []string `json:"targets,omitempty"`
	// Ticket is the backlog ID of the ticket that moves the file.
	Ticket string `json:"ticket"`
	// Note is free text for reviewers.
	Note string `json:"note,omitempty"`
}

// Placeholder is a package that holds doc.go only until a later phase
// implements it.
type Placeholder struct {
	// To is the placeholder directory, read as Package.To is.
	To string `json:"to"`
	// Scope is ScopeRoot (when empty) or ScopeRepo.
	Scope string `json:"scope,omitempty"`
	// Phases are the plan phases that implement it, such as "P10".
	Phases []string `json:"phases"`
	// Note is free text for reviewers.
	Note string `json:"note,omitempty"`
}

// Load reads and decodes the ledger at path. An unknown field, or anything
// but white space after the JSON object, is an error, so a misspelt key
// cannot be silently dropped. Load does not check the entries; Validate
// does.
func Load(path string) (*Ledger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load the ledger: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Ledger
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("load the ledger %s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s continues after the ledger object", ErrInvalid, path)
	}
	return &l, nil
}

// IsLegacy reports whether rel, a slash-separated path relative to the
// repository root, is one of LegacyRoots or lies below one.
func IsLegacy(rel string) bool {
	for _, root := range LegacyRoots {
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

// LegacyRegexp returns the regular expression, anchored at the start, that
// matches a root-relative path below one of LegacyRoots:
// ^(internal|pkg/(aql|blob|cdn|layout|repo|store|types))/. Configuration that
// names the legacy roots as a pattern, such as a lint exclusion, is checked
// against it.
func LegacyRegexp() string {
	return "^" + legacyRoots + "/"
}

// Translate returns the import path that importPath, the import path of a
// legacy package directory, has once its entry is done, together with the
// entry's action. A split or delete entry has no single target, so target is
// empty for them. ok is false when importPath is outside the ledger's module
// or no package entry has its directory as From.
func (l *Ledger) Translate(importPath string) (target, action string, ok bool) {
	rel, found := strings.CutPrefix(importPath, l.Module+"/")
	i := slices.IndexFunc(l.Packages, func(p Package) bool { return p.From == rel })
	if !found || i < 0 {
		return "", "", false
	}
	p := l.Packages[i]
	if p.Action == ActionMove || p.Action == ActionMerge {
		target = l.Module + "/" + l.Dir(p.Scope, p.To)
	}
	return target, p.Action, true
}

// Dir resolves to, the target of an entry with the given scope, to a
// slash-separated path relative to the repository root: to itself under
// ScopeRepo, and to below the staging root otherwise. It does not check to;
// Validate does.
func (l *Ledger) Dir(scope, to string) string {
	if scope == ScopeRepo {
		return to
	}
	return l.StagingRoot + "/" + to
}

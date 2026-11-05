package ledger

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Report holds the status of every ledger entry, derived from a tree by
// Ledger.Status, in ledger order.
type Report struct {
	// Packages pairs each package entry with its status.
	Packages []PackageStatus
	// Files pairs each file entry with its status.
	Files []FileStatus
	// Placeholders pairs each placeholder with its status.
	Placeholders []PlaceholderStatus
}

// PackageStatus is a package entry with its derived status: StatusPending,
// StatusBridged, StatusDone or StatusBroken.
type PackageStatus struct {
	Package
	// Status is the derived status.
	Status string
}

// FileStatus is a file entry with its derived status: StatusPending,
// StatusDone or StatusBroken.
type FileStatus struct {
	File
	// Status is the derived status.
	Status string
}

// PlaceholderStatus is a placeholder with its derived status: StatusOK,
// StatusMissing or StatusImplemented.
type PlaceholderStatus struct {
	Placeholder
	// Status is the derived status.
	Status string
}

// Counts returns how many entries derived each status. Packages and files
// share the pending, bridged, done and broken statuses; placeholders have
// their own, so one map holds all three kinds without mixing them.
func (r Report) Counts() map[string]int {
	counts := map[string]int{}
	for _, p := range r.Packages {
		counts[p.Status]++
	}
	for _, f := range r.Files {
		counts[f.Status]++
	}
	for _, p := range r.Placeholders {
		counts[p.Status]++
	}
	return counts
}

// Status derives the status of every entry from the tree under root, the
// repository root, reading only the directories the entries name:
//
//   - a move or merge package is done when its legacy directory holds no Go
//     files directly and its target holds some, bridged when the legacy
//     directory holds only bridge.go and bridge_*.go files and the target
//     holds Go files, broken when neither directory holds Go files, and
//     pending otherwise;
//   - a split package is done when its legacy directory holds no Go files,
//     bridged when it holds only bridge files, and pending otherwise; a
//     delete package is done or pending in the same way, without bridged;
//   - a file move is done when the legacy path is absent and the target
//     exists, broken when both are absent, and pending otherwise; a file
//     split or delete is done when the legacy path is absent. A trailing
//     "/" on from asks for an asset directory, at the legacy path and at the
//     target alike;
//   - a placeholder is implemented when its directory holds a Go file other
//     than doc.go, ok when it holds doc.go alone, and missing otherwise.
//
// Subdirectories never count: each package directory has its own entry.
// Status expects a ledger that validates. The error reports a directory it
// could not read; a directory that does not exist holds nothing.
func (l *Ledger) Status(root string) (Report, error) {
	t := &tree{root: root}
	r := Report{
		Packages:     make([]PackageStatus, len(l.Packages)),
		Files:        make([]FileStatus, len(l.Files)),
		Placeholders: make([]PlaceholderStatus, len(l.Placeholders)),
	}
	for i, p := range l.Packages {
		r.Packages[i] = PackageStatus{Package: p, Status: l.packageStatus(t, p)}
	}
	for i, f := range l.Files {
		r.Files[i] = FileStatus{File: f, Status: l.fileStatus(t, f)}
	}
	for i, p := range l.Placeholders {
		status := placeholderStatus(t.goFiles(l.dir(p.Scope, p.To)))
		r.Placeholders[i] = PlaceholderStatus{Placeholder: p, Status: status}
	}
	if t.err != nil {
		return Report{}, t.err
	}
	return r, nil
}

func (l *Ledger) packageStatus(t *tree, p Package) string {
	legacy := t.goFiles(p.From)
	if p.Action == ActionSplit || p.Action == ActionDelete {
		switch {
		case len(legacy) == 0:
			return StatusDone
		case p.Action == ActionSplit && onlyBridges(legacy):
			return StatusBridged
		}
		return StatusPending
	}
	target := len(t.goFiles(l.dir(p.Scope, p.To))) > 0
	switch {
	case len(legacy) == 0 && target:
		return StatusDone
	case len(legacy) == 0:
		return StatusBroken
	case target && onlyBridges(legacy):
		return StatusBridged
	}
	return StatusPending
}

func (l *Ledger) fileStatus(t *tree, f File) string {
	gone := !t.exists(f.From)
	switch {
	case !gone:
		return StatusPending
	case f.Action != ActionMove:
		return StatusDone
	case t.exists(l.fileTarget(f)):
		return StatusDone
	}
	return StatusBroken
}

// fileTarget is where a file move puts f: the name in File, or else the last
// element of From, inside the target directory, with the trailing "/" of an
// asset directory kept.
func (l *Ledger) fileTarget(f File) string {
	name := f.File
	if name == "" {
		name = path.Base(strings.TrimSuffix(f.From, "/"))
	}
	target := l.dir(f.Scope, f.To) + "/" + name
	if strings.HasSuffix(f.From, "/") {
		target += "/"
	}
	return target
}

func placeholderStatus(goFiles []string) string {
	switch {
	case slices.ContainsFunc(goFiles, func(name string) bool { return name != "doc.go" }):
		return StatusImplemented
	case len(goFiles) > 0:
		return StatusOK
	}
	return StatusMissing
}

// onlyBridges reports whether every name is a type-alias bridge file. The
// callers have checked that names is not empty.
func onlyBridges(names []string) bool {
	for _, name := range names {
		if !isBridge(name) {
			return false
		}
	}
	return true
}

func isBridge(name string) bool {
	return name == "bridge.go" ||
		(strings.HasPrefix(name, "bridge_") && strings.HasSuffix(name, ".go"))
}

// tree reads directories under a root for Status. The first error it meets
// is kept and later reads return nothing, so Status checks for an error once
// rather than after every directory.
type tree struct {
	root string
	err  error
}

// list returns the entries directly in rel, a slash-separated path relative
// to the root; a directory that does not exist has none.
func (t *tree) list(rel string) []fs.DirEntry {
	if t.err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(t.root, filepath.FromSlash(rel)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.err = fmt.Errorf("derive the ledger status: %w", err)
	}
	return entries
}

// goFiles returns the names of the .go files directly in rel.
func (t *tree) goFiles(rel string) []string {
	var names []string
	for _, e := range t.list(rel) {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	return names
}

// exists reports whether rel names a file, or a directory when rel ends in
// "/". It looks the name up in the parent directory's listing, so the answer
// matches the name's case even on a case-insensitive file system.
func (t *tree) exists(rel string) bool {
	dir := strings.HasSuffix(rel, "/")
	parent, name := path.Split(strings.TrimSuffix(rel, "/"))
	return slices.ContainsFunc(t.list(parent), func(e fs.DirEntry) bool {
		return e.Name() == name && e.IsDir() == dir
	})
}

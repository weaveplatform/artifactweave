package layout

import (
	"cmp"
	"path"
	"strings"
)

// Coordinates are what a layout reads from one file's path: the module
// identity the searches (decision 0012) and the format handlers (decision
// 0032) work with.
type Coordinates struct {
	Org, Module, BaseRev, FolderItegRev, FileItegRev, Classifier, Ext string
}

// Version is the module version the file belongs to: the base revision
// with the folder's integration revision, or the file's when the folder has
// none, as in 1.0-SNAPSHOT.
func (c Coordinates) Version() string {
	return withRev(c.BaseRev, cmp.Or(c.FolderItegRev, c.FileItegRev))
}

// FileVersion is the file's own version: the base revision with the file's
// integration revision, as in 1.0-20260101.120000-1.
func (c Coordinates) FileVersion() string {
	return withRev(c.BaseRev, cmp.Or(c.FileItegRev, c.FolderItegRev))
}

// Integration reports an integration (snapshot) version.
func (c Coordinates) Integration() bool { return c.FolderItegRev != "" || c.FileItegRev != "" }

func withRev(base, rev string) string {
	if rev == "" {
		return base
	}
	return base + "-" + rev
}

// Read returns the coordinates the layout reads from a repository-relative
// file path; ok is false when the path does not fit the layout or lacks one
// of the three fields JFrog's repository-layouts page calls mandatory for
// module identification: organisation, module and base revision
// (ModuleInfo.isValid in 6.20). So a layout without [org] or [orgPath], such
// as cargo-default, reads no module.
func (l *Layout) Read(fullPath string) (Coordinates, bool) {
	v, err := l.Match(fullPath)
	if err != nil {
		return Coordinates{}, false
	}
	c := Coordinates{
		Org: v[TokenOrg], Module: v[TokenModule], BaseRev: v[TokenBaseRev],
		FolderItegRev: v[TokenFolderItegRev], FileItegRev: v[TokenFileItegRev],
		Classifier: v[TokenClassifier], Ext: v[TokenExt],
	}
	// A descriptor pattern spells its extension as a literal, as the Maven
	// layout's .pom does, so the name's is the file's.
	if c.Ext == "" {
		c.Ext = strings.TrimPrefix(path.Ext(fullPath), ".")
	}
	return c, c.Org != "" && c.Module != "" && c.BaseRev != ""
}

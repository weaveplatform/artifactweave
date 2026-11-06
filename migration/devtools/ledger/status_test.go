package ledger

import (
	"maps"
	"path/filepath"
	"testing"
)

// makeTree writes each slash-separated path under a new root; a directory
// exists when a path below it is written.
func makeTree(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		writeFile(t, filepath.Join(root, filepath.FromSlash(p)), "package x\n")
	}
	return root
}

func TestUnitStatusDerivesFromTree(t *testing.T) {
	move := Package{From: "internal/x", Action: ActionMove, To: "platform/x", Ticket: "PLT-03"}
	merge := Package{From: "internal/x", Action: ActionMerge, To: "platform/x", Ticket: "PLT-03"}
	repoMove := Package{
		From:   "internal/x",
		Action: ActionMove,
		Scope:  ScopeRepo,
		To:     "test/server/x",
		Ticket: "TST-01",
	}
	split := Package{From: "internal/x", Action: ActionSplit, Ticket: "PLT-03"}
	del := Package{From: "internal/x", Action: ActionDelete, Ticket: "PRO-02"}
	fileMove := File{
		From:   "internal/x/a.go",
		Action: ActionMove,
		To:     "platform/y",
		Ticket: "PLT-03",
	}
	renamed := File{
		From:   "internal/x/a.go",
		Action: ActionMove,
		To:     "platform/y",
		File:   "b.go",
		Ticket: "PLT-03",
	}
	asset := File{
		From:   "internal/x/testdata/npm/",
		Action: ActionMove,
		To:     "platform/y/testdata",
		Ticket: "PLT-03",
	}
	repoFile := File{
		From:   "internal/x/x_test.go",
		Action: ActionMove,
		Scope:  ScopeRepo,
		To:     "test/server/x",
		Ticket: "TST-05",
	}
	fileSplit := File{
		From:    "internal/x/a.go",
		Action:  ActionSplit,
		Targets: []string{"platform/y", "kernel"},
		Ticket:  "PLT-03",
	}
	fileDelete := File{From: "internal/x/a.go", Action: ActionDelete, Ticket: "PLT-03"}
	placeholder := Placeholder{To: "platform/z", Phases: []string{"P10"}}

	for _, tc := range []struct {
		name  string
		entry any
		paths []string
		want  string
	}{
		{"move pending", move, []string{"internal/x/x.go"}, StatusPending},
		{"move pending with code at both ends", move, []string{"internal/x/x.go", "migration/platform/x/x.go"}, StatusPending},
		{"move pending with bridges but no target", move, []string{"internal/x/bridge.go"}, StatusPending},
		{
			"move bridged", move,
			[]string{"internal/x/bridge.go", "internal/x/bridge_kernel.go", "migration/platform/x/x.go"},
			StatusBridged,
		},
		{
			"move pending with a bridge and other code", move,
			[]string{"internal/x/bridge.go", "internal/x/bridges.go", "migration/platform/x/x.go"},
			StatusPending,
		},
		{
			"move done with a subpackage left", move,
			[]string{"internal/x/sub/sub.go", "migration/platform/x/x.go"},
			StatusDone,
		},
		{"move broken", move, []string{"internal/x/README.md"}, StatusBroken},
		{"merge done", merge, []string{"migration/platform/x/x.go"}, StatusDone},
		{"repo-scope move done", repoMove, []string{"test/server/x/x_test.go"}, StatusDone},

		{"split pending", split, []string{"internal/x/x.go", "internal/x/bridge.go"}, StatusPending},
		{"split bridged", split, []string{"internal/x/bridge_platform.go"}, StatusBridged},
		{"split done", split, nil, StatusDone},
		{"delete pending, as bridges are Go files too", del, []string{"internal/x/bridge.go"}, StatusPending},
		{"delete done", del, []string{"internal/x/testdata/x.go"}, StatusDone},

		{"file move pending", fileMove, []string{"internal/x/a.go", "migration/platform/y/a.go"}, StatusPending},
		{"file move done", fileMove, []string{"migration/platform/y/a.go"}, StatusDone},
		{"file move broken", fileMove, nil, StatusBroken},
		{"file move broken by a directory at the target", fileMove, []string{"migration/platform/y/a.go/x.go"}, StatusBroken},
		{"renamed file move done", renamed, []string{"migration/platform/y/b.go"}, StatusDone},
		{"renamed file move broken", renamed, []string{"migration/platform/y/a.go"}, StatusBroken},
		{"asset directory pending", asset, []string{"internal/x/testdata/npm/p.json"}, StatusPending},
		{"asset directory done", asset, []string{"migration/platform/y/testdata/npm/p.json"}, StatusDone},
		{"repo-scope file move done", repoFile, []string{"test/server/x/x_test.go"}, StatusDone},
		{"file split pending", fileSplit, []string{"internal/x/a.go"}, StatusPending},
		{"file split done", fileSplit, []string{"migration/platform/y/a.go"}, StatusDone},
		{"file delete done", fileDelete, nil, StatusDone},

		{
			"placeholder ok, ignoring subdirectories and other files", placeholder,
			[]string{"migration/platform/z/doc.go", "migration/platform/z/sub/sub.go", "migration/platform/z/README.md"},
			StatusOK,
		},
		{"placeholder missing", placeholder, nil, StatusMissing},
		{"placeholder missing its doc.go", placeholder, []string{"migration/platform/z/README.md"}, StatusMissing},
		{
			"placeholder implemented", placeholder,
			[]string{"migration/platform/z/doc.go", "migration/platform/z/z.go"},
			StatusImplemented,
		},
		{
			"placeholder implemented by a test", placeholder,
			[]string{"migration/platform/z/doc.go", "migration/platform/z/z_test.go"},
			StatusImplemented,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Ledger{
				Schema:      schemaVersion,
				Module:      module,
				StagingRoot: stagingRoot,
				FinalRoot:   finalRoot,
			}
			switch e := tc.entry.(type) {
			case Package:
				l.Packages = []Package{e}
			case File:
				l.Files = []File{e}
			case Placeholder:
				l.Placeholders = []Placeholder{e}
			}
			r, err := l.Status(makeTree(t, tc.paths...))
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Counts(); !maps.Equal(got, map[string]int{tc.want: 1}) {
				t.Errorf("Status counts = %v; want one %s", got, tc.want)
			}
		})
	}
}

func TestUnitStatusReportsEntriesInLedgerOrder(t *testing.T) {
	l := sample()
	root := makeTree(t,
		"internal/server/server.go",
		"internal/server/handlers.go",
		"internal/server/npm_test.go",
		"internal/server/testdata/npm/package.json",
		"migration/artifacts/ecosystem/npm/http/npm.go",
		"migration/artifacts/ecosystem/registry.go",
		"internal/layout/layout_test.go",
		"internal/a/a.go",
		"internal/b/b.go",
		"test/server/support/fake/fake.go",
		"migration/artifacts/core/virtual/doc.go",
	)
	r, err := l.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{StatusPending: 7, StatusDone: 4, StatusOK: 1}
	if got := r.Counts(); !maps.Equal(got, want) {
		t.Errorf("Counts = %v; want %v", got, want)
	}
	for i, p := range r.Packages {
		if p.From != l.Packages[i].From {
			t.Errorf(
				"report package %d is %s; want ledger order (%s)",
				i,
				p.From,
				l.Packages[i].From,
			)
		}
	}
	if r.Packages[1].Status != StatusDone || r.Files[3].Status != StatusDone ||
		r.Placeholders[0].Status != StatusOK {
		t.Errorf("statuses = %+v", r)
	}
}

func TestUnitStatusFailsOnAnUnreadableDirectory(t *testing.T) {
	// A regular file where a directory belongs fails the status, unlike a
	// path that does not exist. The message is the same on every operating
	// system, though Windows reports reading through the file as not found.
	root := makeTree(t, "internal/x", "migration/platform/z")
	for _, tc := range []struct {
		l    *Ledger
		file string
	}{
		{&Ledger{Packages: []Package{{From: "internal/x", Action: ActionMove, To: "platform/x"}}}, "internal/x"},
		{&Ledger{Files: []File{{From: "internal/x/a.go", Action: ActionDelete}}}, "internal/x"},
		{&Ledger{Placeholders: []Placeholder{{To: "platform/z"}}}, "migration/platform/z"},
	} {
		tc.l.StagingRoot = stagingRoot
		r, err := tc.l.Status(root)
		want := "derive the ledger status: " + tc.file + " is not a directory"
		empty := r.Packages == nil && r.Files == nil && r.Placeholders == nil
		if err == nil || err.Error() != want || !empty {
			t.Errorf("Status = %+v, %v; want only the error %q", r, err, want)
		}
	}
}

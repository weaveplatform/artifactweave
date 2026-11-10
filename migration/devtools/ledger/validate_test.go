package ledger

import (
	"errors"
	"strings"
	"testing"
)

func TestUnitValidateRejectsInvalidEntries(t *testing.T) {
	if errs := sample().Validate(); errs != nil {
		t.Fatalf("the sample ledger does not validate: %v", errs)
	}

	// Each case breaks one rule of a valid ledger and expects exactly the
	// one error that rule reports.
	for _, tc := range []struct {
		name   string
		change func(l *Ledger)
		want   string
	}{
		{"schema version", func(l *Ledger) { l.Schema = 2 }, "schema is 2, not 1"},
		{"module", func(l *Ledger) { l.Module = "example.com/other" }, `module is "example.com/other"`},
		{"staging root", func(l *Ledger) { l.StagingRoot = "staged" }, `staging_root is "staged"`},
		{"final root", func(l *Ledger) { l.FinalRoot = "src" }, `final_root is "src"`},

		{
			"package from outside the legacy roots", func(l *Ledger) { l.Packages[3].From = "pkg/client/access" },
			`package "pkg/client/access": from "pkg/client/access" does not match`,
		},
		{
			"package from with a capital", func(l *Ledger) { l.Packages[3].From = "internal/Layout" },
			`from "internal/Layout" does not match`,
		},
		{
			"package action", func(l *Ledger) { l.Packages[1].Action = "rename" },
			`action "rename" is not one of move, merge, split, delete`,
		},
		{"package ticket", func(l *Ledger) { l.Packages[1].Ticket = "ECO-6" }, `ticket "ECO-6" does not match`},
		{"package ticket area", func(l *Ledger) { l.Packages[1].Ticket = "XYZ-06" }, `ticket "XYZ-06" does not match`},
		{"package scope", func(l *Ledger) { l.Packages[1].Scope = "module" }, `scope "module" is not one of root, repo`},
		{"package name", func(l *Ledger) { l.Packages[1].Name = "npm_http" }, `name "npm_http" does not match`},
		{
			"rename key", func(l *Ledger) { l.Packages[2].Rename = map[string]string{"../formats.go": "registry.go"} },
			`rename key "../formats.go" does not match`,
		},
		{
			"rename value", func(l *Ledger) { l.Packages[2].Rename = map[string]string{"formats.go": "sub/registry.go"} },
			`rename value "sub/registry.go" does not match`,
		},
		{"move without to", func(l *Ledger) { l.Packages[2].To = "" }, `package "internal/formatregistry": to is empty`},
		{
			"target pattern", func(l *Ledger) { l.Packages[1].To = "artifacts/ecosystem/npm/HTTP" },
			`to "artifacts/ecosystem/npm/HTTP" does not match`,
		},
		{
			"target top level", func(l *Ledger) { l.Packages[1].To = "server/npm" },
			`to "server/npm" does not start with a staged top-level directory`,
		},
		{
			"repo-scope target outside test", func(l *Ledger) { l.Packages[6].To = "tools/fake" },
			`repo-scope to "tools/fake" does not start with test/`,
		},
		{"split with to", func(l *Ledger) { l.Packages[0].To = "app" }, "a split entry has no to, name or rename"},
		{"split with name", func(l *Ledger) { l.Packages[0].Name = "server" }, "a split entry has no to, name or rename"},
		{
			"delete with rename", func(l *Ledger) { l.Packages[3].Rename = map[string]string{} },
			"a delete entry has no to, name or rename",
		},

		{
			"file from", func(l *Ledger) { l.Files[0].From = "internal/web/xyz test.go" },
			`file "internal/web/xyz test.go": from "internal/web/xyz test.go" does not match`,
		},
		{
			"file action", func(l *Ledger) { l.Files[0].Action = ActionMerge },
			`action "merge" is not one of move, split, delete`,
		},
		{"file ticket", func(l *Ledger) { l.Files[0].Ticket = "" }, `ticket "" does not match`},
		{"file scope", func(l *Ledger) { l.Files[1].Scope = "repository" }, `scope "repository" is not one of`},
		{"file name", func(l *Ledger) { l.Files[0].File = "npm/npm_test.go" }, `file "npm/npm_test.go" does not match`},
		{"file move without to", func(l *Ledger) { l.Files[1].To = "" }, "to is empty"},
		{"file to on a delete", func(l *Ledger) { l.Files[3].To = "Bad" }, `to "Bad" does not match`},
		{
			"split file with one target", func(l *Ledger) { l.Files[2].Targets = l.Files[2].Targets[:1] },
			"targets has 1 entries, fewer than 2",
		},
		{"split file without targets", func(l *Ledger) { l.Files[2].Targets = nil }, "targets has 0 entries"},
		{
			"split file target", func(l *Ledger) { l.Files[2].Targets[1] = "access/Adapters" },
			`targets "access/Adapters" does not match`,
		},
		{"empty split file target", func(l *Ledger) { l.Files[2].Targets[1] = "" }, "targets is empty"},
		{
			"file outside any package entry", func(l *Ledger) { l.Files[3].From = "internal/other/legacy.go" },
			`file "internal/other/legacy.go": no package entry covers its directory`,
		},

		{"placeholder without to", func(l *Ledger) { l.Placeholders[0].To = "" }, `placeholder "": to is empty`},
		{"placeholder scope", func(l *Ledger) { l.Placeholders[0].Scope = "staged" }, `scope "staged" is not one of`},
		{"placeholder without phases", func(l *Ledger) { l.Placeholders[0].Phases = nil }, "phases has no entry"},
		{
			"placeholder phase", func(l *Ledger) { l.Placeholders[0].Phases = []string{"10"} },
			`phase "10" does not match`,
		},

		{
			"repeated package from", func(l *Ledger) { l.Packages = append(l.Packages, l.Packages[3]) },
			`from "internal/layout" appears in 2 entries`,
		},
		{"file from repeats a package from", func(l *Ledger) {
			l.Files = append(l.Files, File{From: "internal/web/xyz", Action: ActionDelete, Ticket: "ECO-06"})
		}, `from "internal/web/xyz" appears in 2 entries`},
		{
			"repeated placeholder", func(l *Ledger) { l.Placeholders = append(l.Placeholders, l.Placeholders[0]) },
			"placeholder migration/artifacts/core/virtual appears 2 times",
		},
		{"move target shared", func(l *Ledger) {
			l.Packages = append(l.Packages, Package{
				From: "internal/c", Action: ActionMerge, To: "artifacts/ecosystem/npm/http", Ticket: "ECO-06",
			})
		}, `package "internal/web/xyz": move target migration/artifacts/ecosystem/npm/http is shared`},
		{
			"merge target alone", func(l *Ledger) { l.Packages = l.Packages[:5] },
			`package "internal/a": merge target migration/platform/ab has no other entry`,
		},
		{
			"package targets a placeholder", func(l *Ledger) { l.Packages[2].To = "artifacts/core/virtual" },
			`placeholder "artifacts/core/virtual": it is the target of an entry`,
		},
		{
			"file targets a placeholder", func(l *Ledger) { l.Files[2].Targets[0] = "artifacts/core/virtual" },
			`placeholder "artifacts/core/virtual": it is the target of an entry`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := sample()
			tc.change(l)
			errs := l.Validate()
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("Validate = %q; want one error containing %q", errs, tc.want)
			}
			if !errors.Is(errs[0], ErrInvalid) {
				t.Errorf("%v does not wrap ErrInvalid", errs[0])
			}
		})
	}
}

func TestUnitValidateReportsEveryErrorInOrder(t *testing.T) {
	l := sample()
	l.Schema = 0
	l.Packages[1].Ticket = "ECO-6"
	l.Placeholders[0].Phases = nil
	errs := l.Validate()
	want := []string{
		"schema is 0",
		`package "internal/web/xyz": ticket`,
		`placeholder "artifacts/core/virtual": phases`,
	}
	if len(errs) != len(want) {
		t.Fatalf("Validate = %q; want %d errors", errs, len(want))
	}
	for i, w := range want {
		if !strings.Contains(errs[i].Error(), w) {
			t.Errorf("error %d = %q; want it to contain %q", i, errs[i], w)
		}
	}
}

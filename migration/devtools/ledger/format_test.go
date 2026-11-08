package ledger

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestUnitFormatIsCanonicalAndIdempotent(t *testing.T) {
	l := sample()
	l.Packages[3].Note = "the staged layout tests replace it; see <migration/README.md> & the plan"
	l.Placeholders = append(
		l.Placeholders,
		Placeholder{To: "access/core/scim", Phases: []string{"P22", "P41"}},
	)
	before := slices.Clone(l.Packages)

	got := string(Format(l))
	want := `{
    "schema": 1,
    "module": "github.com/weaveplatform/artifactweave",
    "staging_root": "migration",
    "final_root": "internal",
    "packages": [
        {"from":"internal/a","action":"merge","to":"platform/ab","ticket":"PLT-03"},
        {"from":"internal/b","action":"merge","to":"platform/ab","ticket":"PLT-03"},
        {"from":"internal/fakeserver","action":"move","to":"test/server/support/fake","scope":"repo","ticket":"TST-01"},
        {"from":"internal/formatregistry","action":"move","to":"artifacts/ecosystem","rename":{"formats.go":"registry.go"},"ticket":"ART-05"},
        {"from":"internal/layout","action":"delete","ticket":"PRO-02","note":"the staged layout tests replace it; see <migration/README.md> & the plan"},
        {"from":"internal/server","action":"split","ticket":"WEB-22"},
        {"from":"migration/artifacts/ecosystem/npm/http","action":"move","to":"artifacts/ecosystem/npm/http","name":"npmhttp","ticket":"ECO-06"}
    ],
    "files": [
        {"from":"internal/server/handlers.go","action":"split","targets":["artifacts/adapters/http","access/adapters/http"],"ticket":"WEB-03"},
        {"from":"internal/server/legacy.go","action":"delete","ticket":"WEB-22"},
        {"from":"internal/server/npm_test.go","action":"move","to":"test/server/artifacts/npm","scope":"repo","ticket":"TST-05"},
        {"from":"internal/server/testdata/npm/","action":"move","to":"artifacts/ecosystem/npm/http/testdata","ticket":"ECO-06"}
    ],
    "placeholders": [
        {"to":"access/core/scim","phases":["P22","P41"]},
        {"to":"artifacts/core/virtual","phases":["P10"]}
    ]
}
`
	if got != want {
		t.Fatalf("Format =\n%s\nwant\n%s", got, want)
	}
	if !reflect.DeepEqual(l.Packages, before) {
		t.Error("Format reordered the ledger it was given")
	}

	// Loading the canonical bytes gives the sorted ledger back, and
	// formatting that again changes nothing. The strict decoder also proves
	// that Format's hard-written header keys are the struct's JSON names.
	path := filepath.Join(t.TempDir(), "ledger.json")
	writeFile(t, path, got)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	sorted := *l
	sorted.Packages = sortedBy(l.Packages, func(p Package) string { return p.From })
	sorted.Files = sortedBy(l.Files, func(f File) string { return f.From })
	sorted.Placeholders = sortedBy(l.Placeholders, func(p Placeholder) string { return p.To })
	if !reflect.DeepEqual(loaded, &sorted) {
		t.Errorf("Load(Format(l)) = %+v; want %+v", loaded, &sorted)
	}
	if again := string(Format(loaded)); again != got {
		t.Errorf("Format is not idempotent:\n%s", again)
	}

	// Nil and empty lists both print as [], as the schema requires the keys.
	empty := `{
    "schema": 1,
    "module": "github.com/weaveplatform/artifactweave",
    "staging_root": "migration",
    "final_root": "internal",
    "packages": [],
    "files": [],
    "placeholders": []
}
`
	header := &Ledger{
		Schema:      schemaVersion,
		Module:      module,
		StagingRoot: stagingRoot,
		FinalRoot:   finalRoot,
	}
	if got := string(Format(header)); got != empty {
		t.Errorf("Format of a ledger with nil lists =\n%s", got)
	}
	header.Files = []File{}
	if got := string(Format(header)); got != empty {
		t.Errorf("Format of a ledger with an empty list =\n%s", got)
	}
}

package terraformbackend_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"app-dev", "jfrog-k8s", "a__b", "Ünïcode", "a.b", "a;b"} {
		if err := tfbe.CheckName(ok); err != nil {
			t.Errorf("CheckName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a b", "a\tb", "a\nb", `a\b`, "a\x00b", "a b"} {
		if err := tfbe.CheckName(bad); !errors.Is(err, tfbe.ErrInvalidName) {
			t.Errorf("CheckName(%q) = %v", bad, err)
		}
	}
}

func TestPaths(t *testing.T) {
	if got := tfbe.Folder("app"); got != "workspaces/app" {
		t.Errorf("Folder = %s", got)
	}
	if got := tfbe.RecordPath("app"); got != "workspaces/app/workspace.json" {
		t.Errorf("RecordPath = %s", got)
	}
	if got := tfbe.LatestPath("app"); got != "workspaces/app/state.latest.json" {
		t.Errorf("LatestPath = %s", got)
	}
	if got := tfbe.StatePath(
		"app",
		1640018380463,
	); got != "workspaces/app/state.1640018380463.json" {
		t.Errorf("StatePath = %s", got)
	}
	if got := tfbe.Row("tf", "app"); got != "tf/workspaces/app" {
		t.Errorf("Row = %s", got)
	}
	if got := tfbe.Created(1640018380463); !got.Equal(time.UnixMilli(1640018380463)) ||
		got.Location() != time.UTC {
		t.Errorf("Created = %v", got)
	}
}

func TestParseFile(t *testing.T) {
	for p, want := range map[string]tfbe.File{
		"workspaces/app/state.latest.json":         {Workspace: "app", Latest: true},
		"/workspaces/app/state.1640018380463.json": {Workspace: "app", Ms: 1640018380463},
	} {
		if got, ok := tfbe.ParseFile(p); !ok || got != want {
			t.Errorf("ParseFile(%q) = %+v, %v", p, got, ok)
		}
	}
	for _, p := range []string{
		"workspaces/app/workspace.json", "workspaces/app/state.json", "workspaces/app/state.01.json",
		"workspaces/app/state.0.json", "workspaces/app/state.-1.json", "workspaces/app/state.x.json",
		"workspaces/app/state.1.txt", "workspaces/state.latest.json", "other/app/state.latest.json",
		"workspaces/app/sub/state.latest.json", "workspaces//state.latest.json",
		"workspaces/app/state.99999999999999999999.json", "workspaces/app/1.json",
		"workspaces/app/state.1",
	} {
		if f, ok := tfbe.ParseFile(p); ok {
			t.Errorf("ParseFile(%q) = %+v", p, f)
		}
	}
	created := time.UnixMilli(1700000000123)
	latest := types.Item{Name: "workspaces/app/state.latest.json", Created: created}
	if f, ok := tfbe.FileOf(latest); !ok || f.Ms != 1700000000123 || !f.Latest {
		t.Errorf("FileOf(latest) = %+v, %v", f, ok)
	}
	if _, ok := tfbe.FileOf(
		types.Item{Name: "workspaces/app/state.latest.json", Folder: true},
	); ok {
		t.Error("a folder is a state file")
	}
}

func TestIDs(t *testing.T) {
	if got := tfbe.WorkspaceID("tf-local", "app"); got != "tf-local__app" {
		t.Errorf("WorkspaceID = %s", got)
	}
	if got := tfbe.StateID("tf-local", "app", 12); got != "tf-local__app__12" {
		t.Errorf("StateID = %s", got)
	}
	if got := tfbe.Splits(
		"a__b__c",
	); !slices.Equal(
		got,
		[][2]string{{"a", "b__c"}, {"a__b", "c"}},
	) {
		t.Errorf("Splits = %v", got)
	}
	if got := tfbe.Splits("a___b"); !slices.Equal(got, [][2]string{{"a", "_b"}, {"a_", "b"}}) {
		t.Errorf("Splits(a___b) = %v", got)
	}
	for _, none := range []string{"ab", "__ab", "ab__", "__"} {
		if got := tfbe.Splits(none); len(got) != 0 {
			t.Errorf("Splits(%q) = %v", none, got)
		}
	}
	for id, want := range map[string]string{
		"tf__app__12":   "tf__app",
		"tf__ws___12":   "tf__ws_",
		"tf__a__b__123": "tf__a__b",
	} {
		ws, ms, ok := tfbe.SplitStateID(id)
		if !ok || ws != want || ms <= 0 {
			t.Errorf("SplitStateID(%q) = %s, %d, %v", id, ws, ms, ok)
		}
	}
	for _, bad := range []string{"tf", "__12", "tf__app__", "tf__app__x", "tf__app__012", "tf__app__0"} {
		if _, _, ok := tfbe.SplitStateID(bad); ok {
			t.Errorf("SplitStateID(%q) succeeded", bad)
		}
	}
}

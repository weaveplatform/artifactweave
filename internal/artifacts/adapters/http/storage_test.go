package artifactshttp

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// The permissions view renders what Grants answers in the legacy letters,
// leaving out a name that holds no action. It asks about the item's full
// path through the item's repository, then ANY, then the ANY LOCAL or ANY
// REMOTE target that the repository's class adds, and passes a Grants error
// up as it came, since ItemGrants names the target it was reading.
func TestUnitStoragePermissionsRenderGrants(t *testing.T) {
	var asked []string
	grants := &accessapitest.Grants{ItemGrantsFunc: func(
		_ context.Context,
		targets []string,
		path string,
		folder bool,
	) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
		if path != "dir/a.txt" || folder {
			t.Errorf("ItemGrants asked about %q (folder %t), want the file dir/a.txt", path, folder)
		}
		asked = targets
		users := map[string]accessapi.Actions{
			"dev":  accessapi.ActionRead | accessapi.ActionWrite,
			"none": 0,
		}
		return users, map[string]accessapi.Actions{"readers": accessapi.ActionRead}, nil
	}}
	v := storageView{
		s:   newArtifactoryAPI(Config{Access: accessapi.API{Grants: grants}}),
		key: "libs",
	}
	item := model.Item{RepoKey: "libs", Path: "dir", Name: "a.txt"}
	want := map[string]any{
		"uri": "/artifactory/api/storage/libs/dir/a.txt",
		"principals": map[string]any{
			"users":  map[string][]string{"dev": {"r", "w"}},
			"groups": map[string][]string{"readers": {"r"}},
		},
	}
	for class, targets := range map[repo.Class][]string{
		repo.Local:     {"libs", accessapi.TargetAny, accessapi.TargetAnyLocal},
		repo.Federated: {"libs", accessapi.TargetAny, accessapi.TargetAnyLocal},
		repo.Remote:    {"libs", accessapi.TargetAny, accessapi.TargetAnyRemote},
		repo.Virtual:   {"libs", accessapi.TargetAny},
	} {
		f := itemkit.Found{Repo: repo.Config{Key: "libs", Class: class}, Item: item}
		media, body, err := v.permissions(t.Context(), f)
		if err != nil || media != "application/json" || !reflect.DeepEqual(body, want) {
			t.Errorf("%s: %q, %v, %v; want %v", class, media, body, err, want)
		}
		if !slices.Equal(asked, targets) {
			t.Errorf("%s: asked about %q, want %q", class, asked, targets)
		}
	}
	grants.ItemGrantsFunc = func(
		context.Context, []string, string, bool,
	) (map[string]accessapi.Actions, map[string]accessapi.Actions, error) {
		return nil, nil, errBoom
	}
	f := itemkit.Found{Repo: repo.Config{Key: "libs", Class: repo.Local}, Item: item}
	if _, _, err := v.permissions(t.Context(), f); !errors.Is(err, errBoom) ||
		err.Error() != errBoom.Error() {
		t.Errorf("a Grants error: %v, want it as Grants gave it", err)
	}
}

func TestUnitParseProperties(t *testing.T) {
	for in, want := range map[string]model.Properties{
		`a=1`:          {"a": {"1"}},
		`a=1,2|b=`:     {"a": {"1", "2"}, "b": {""}},
		`a=x=y;a=z`:    {"a": {"x=y", "z"}},
		`a=p\;q\|r`:    {"a": {"p;q|r"}},
		`a\b=1`:        {"ab": {"1"}},
		`;;a=1;`:       {"a": {"1"}},
		`a=trailing\`:  {"a": {`trailing\`}},
		`=1`:           nil,
		`a=1;missingv`: nil,
	} {
		got, err := parseProperties(in)
		if want == nil {
			if err == nil {
				t.Errorf("%q: parsed %v, want an error", in, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, %v, want %v", in, got, err, want)
		}
	}
}

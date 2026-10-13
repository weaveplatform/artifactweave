package types_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types"
	"github.com/weaveplatform/weaveplatform-artifact-registry/pkg/types/wire"
)

func TestActionsSpellings(t *testing.T) {
	all := types.ActionRead | types.ActionAnnotate | types.ActionWrite | types.ActionDelete |
		types.ActionManage | types.ActionScan | types.ActionExecute
	if diff := cmp.Diff(
		[]string{"READ", "ANNOTATE", "WRITE", "DELETE", "MANAGE", "SCAN", "EXECUTE"},
		all.AccessNames(),
	); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff(
		[]string{"read", "annotate", "write", "delete", "manage", "managedXrayMeta", "distribute"},
		all.LegacyNames(),
	); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff([]string{"r", "n", "w", "d", "m", "mxm", "x"}, all.Letters()); diff != "" {
		t.Error(diff)
	}
	if a, err := types.ParseAccessActions(
		[]string{"read", "Write"},
	); err != nil ||
		a != types.ActionRead|types.ActionWrite {
		t.Errorf("access names are case-insensitive: %v %v", a, err)
	}
	for _, bad := range []func() error{
		func() error { _, err := types.ParseAccessActions([]string{"FLY"}); return err },
		func() error { _, err := types.ParseLegacyActions([]string{"READ"}); return err },
		func() error { _, err := types.ParseLetters([]string{"q"}); return err },
	} {
		if err := bad(); !errors.Is(err, types.ErrUnknownAction) {
			t.Errorf("want ErrUnknownAction, got %v", err)
		}
	}
}

func TestActionsJSON(t *testing.T) {
	b, err := json.Marshal(types.ActionRead | types.ActionScan)
	if err != nil || string(b) != `["READ","SCAN"]` {
		t.Fatalf("%s %v", b, err)
	}
	if b, _ := json.Marshal(types.Actions(0)); string(b) != `[]` {
		t.Fatalf("empty actions: %s", b)
	}
	var a types.Actions
	if err := json.Unmarshal(
		[]byte(`["delete","MANAGE"]`),
		&a,
	); err != nil ||
		a != types.ActionDelete|types.ActionManage {
		t.Fatalf("%v %v", a, err)
	}
	if err := json.Unmarshal([]byte(`["nope"]`), &a); !errors.Is(err, types.ErrUnknownAction) {
		t.Fatalf("want ErrUnknownAction, got %v", err)
	}
	if err := json.Unmarshal([]byte(`"READ"`), &a); err == nil {
		t.Fatal("a non-array should not decode")
	}
}

// The permission document is persisted as JSON; it must come back intact.
func TestPermissionResourcesDocument(t *testing.T) {
	in := map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{
				Users: map[string]types.Actions{"alice": types.ActionRead | types.ActionWrite},
			},
			Targets: map[string]types.Target{
				types.TargetAnyLocal: {Include: []string{"**"}, Exclude: []string{"*.tmp"}},
			},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[types.ResourceType]types.Resource
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Error(diff)
	}
	p := types.Permission{
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceBuild:    {},
			types.ResourceArtifact: {},
		},
	}
	if diff := cmp.Diff(
		[]types.ResourceType{types.ResourceArtifact, types.ResourceBuild},
		p.ResourceTypes(),
	); diff != "" {
		t.Error(diff)
	}
}

func TestMatchesRepository(t *testing.T) {
	cases := []struct {
		target, key, class string
		want               bool
	}{
		{types.TargetAny, "x", "remote", true},
		{types.TargetAnyLocal, "x", "local", true},
		{types.TargetAnyLocal, "x", "federated", true},
		{types.TargetAnyLocal, "x", "remote", false},
		{types.TargetAnyRemote, "x", "remote", true},
		{types.TargetAnyRemote, "x", "virtual", false},
		{types.TargetAnyDistribution, "x", "distribution", true},
		{"libs", "libs", "local", true},
		{"libs", "other", "local", false},
	}
	for _, c := range cases {
		if got := types.MatchesRepository(c.target, c.key, c.class); got != c.want {
			t.Errorf("%q %q %q = %v", c.target, c.key, c.class, got)
		}
	}
}

func TestPermissionV1RoundTrip(t *testing.T) {
	cases := []wire.PermissionTargetV1{
		{
			Name:            "populate",
			IncludesPattern: "**",
			Repositories:    []string{"ANY REMOTE", "libs-release-local"},
			Principals: wire.PrincipalsV1{
				Users:  map[string][]string{"bob": {"r", "w", "mxm"}},
				Groups: map[string][]string{"readers": {"r"}},
			},
		},
		{
			Name:            "patterns",
			IncludesPattern: "org/**,com/**",
			ExcludesPattern: "**/*.tmp",
			Repositories:    []string{"ANY LOCAL"},
			Principals: wire.PrincipalsV1{
				Users: map[string][]string{"x": {"r", "n", "d", "m", "x"}},
			},
		},
		{Name: "empty", IncludesPattern: "**", Repositories: []string{}},
	}
	for _, in := range cases {
		p, err := types.PermissionFromV1(in)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		out, err := types.PermissionToV1(p)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if diff := cmp.Diff(in, out); diff != "" {
			t.Errorf("%s round trip (-in +out):\n%s", in.Name, diff)
		}
	}
	// An omitted include pattern means "**".
	p, err := types.PermissionFromV1(
		wire.PermissionTargetV1{Name: "d", Repositories: []string{"r"}},
	)
	if err != nil ||
		!cmp.Equal(p.Resources[types.ResourceArtifact].Targets["r"].Include, []string{"**"}) {
		t.Errorf("default include: %+v %v", p, err)
	}
	if _, err := types.PermissionFromV1(wire.PermissionTargetV1{
		Principals: wire.PrincipalsV1{Groups: map[string][]string{"g": {"zz"}}},
	}); !errors.Is(err, types.ErrUnknownAction) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

func TestPermissionV2RoundTrip(t *testing.T) {
	in := wire.PermissionTargetV2{
		Name: "java-developers",
		Repo: &wire.SectionV2{
			IncludePatterns: []string{"**"},
			ExcludePatterns: []string{},
			Repositories:    []string{"ANY LOCAL", "libs-release-local"},
			Actions: wire.ActionsV2{
				Users:  map[string][]string{"bob": {"read", "write", "managedXrayMeta"}},
				Groups: map[string][]string{"dev": {"read"}},
			},
		},
		Build: &wire.SectionV2{
			IncludePatterns: []string{"**"},
			ExcludePatterns: []string{"*.js"},
			Repositories:    []string{"artifactory-build-info"},
			Actions: wire.ActionsV2{
				Groups: map[string][]string{"ci": {"read", "annotate", "delete", "manage"}},
			},
		},
		ReleaseBundle: &wire.SectionV2{
			IncludePatterns: []string{"**"},
			ExcludePatterns: []string{},
			Repositories:    []string{},
			Actions:         wire.ActionsV2{Users: map[string][]string{"rel": {"distribute"}}},
		},
	}
	p, err := types.PermissionFromV2(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := types.PermissionToV2(p)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	// The legacy API emits [""] to mean "no exclusions".
	p, err = types.PermissionFromV2(wire.PermissionTargetV2{Name: "x", Repo: &wire.SectionV2{
		IncludePatterns: []string{""}, ExcludePatterns: []string{""}, Repositories: []string{"r"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Resources[types.ResourceArtifact].Targets["r"]; !cmp.Equal(
		got,
		types.Target{Include: []string{"**"}},
	) {
		t.Errorf("empty patterns: %+v", got)
	}
	if _, err := types.PermissionFromV2(wire.PermissionTargetV2{Build: &wire.SectionV2{
		Actions: wire.ActionsV2{Users: map[string][]string{"u": {"r"}}},
	}}); !errors.Is(err, types.ErrUnknownAction) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

func TestLegacyProjectionsRefuseMixedPatterns(t *testing.T) {
	p := types.Permission{Name: "mixed", Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {Targets: map[string]types.Target{
			"a": {Include: []string{"**"}},
			"b": {Include: []string{"org/**"}},
		}},
	}}
	if _, err := types.PermissionToV1(p); !errors.Is(err, types.ErrLossyProjection) {
		t.Errorf("v1: want ErrLossyProjection, got %v", err)
	}
	if _, err := types.PermissionToV2(p); !errors.Is(err, types.ErrLossyProjection) {
		t.Errorf("v2: want ErrLossyProjection, got %v", err)
	}
	// Non-repository sections are simply absent from the v1 view.
	p = types.Permission{
		Name:      "b",
		Resources: map[types.ResourceType]types.Resource{types.ResourceBuild: {}},
	}
	v1, err := types.PermissionToV1(p)
	if err != nil || len(v1.Repositories) != 0 || v1.IncludesPattern != "**" {
		t.Errorf("%+v %v", v1, err)
	}
}

func TestPermissionAccessRoundTrip(t *testing.T) {
	in := wire.AccessPermission{
		Name:        "my-permission",
		Description: "d",
		Resources: map[string]wire.AccessResource{
			"artifact": {
				Actions: wire.AccessActions{
					Users: map[string][]string{"alice": {"READ", "WRITE"}},
					Groups: map[string][]string{
						"devs": {"READ", "ANNOTATE", "WRITE", "DELETE", "MANAGE", "SCAN"},
					},
				},
				Targets: map[string]wire.AccessTarget{
					"ANY LOCAL": {
						IncludePatterns: []string{"**"},
						ExcludePatterns: []string{},
					},
					"my-docker-local": {
						IncludePatterns: []string{"**"},
						ExcludePatterns: []string{"*.tmp"},
					},
				},
			},
			"destination": {
				Actions: wire.AccessActions{
					Groups: map[string][]string{"g": {"DELETE", "MANAGE", "EXECUTE"}},
				},
				Targets: map[string]wire.AccessTarget{
					"*": {IncludePatterns: []string{}, ExcludePatterns: []string{}},
				},
			},
		},
	}
	p, err := types.PermissionFromAccess(in)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, types.PermissionToAccess(p)); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	if _, err := types.PermissionFromAccess(
		wire.AccessPermission{Resources: map[string]wire.AccessResource{
			"artifact": {Actions: wire.AccessActions{Users: map[string][]string{"u": {"FLY"}}}},
		}},
	); !errors.Is(
		err,
		types.ErrUnknownAction,
	) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

// One internal permission reads the same through all three wire forms when
// it is representable in all of them.
func TestPermissionAcrossWireForms(t *testing.T) {
	v1 := wire.PermissionTargetV1{
		Name:            "p",
		IncludesPattern: "**",
		Repositories:    []string{"ANY"},
		Principals:      wire.PrincipalsV1{Users: map[string][]string{"u": {"r", "w"}}},
	}
	p1, err := types.PermissionFromV1(v1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := types.PermissionFromAccess(types.PermissionToAccess(p1))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := types.PermissionToV2(p2)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := types.PermissionFromV2(v2)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(p1, p3); diff != "" {
		t.Errorf("(-v1 +v2):\n%s", diff)
	}
}

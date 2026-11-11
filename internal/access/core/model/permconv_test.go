package model_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
)

// The permission document is persisted as JSON; it must come back intact.
func TestUnitPermissionResourcesDocument(t *testing.T) {
	in := map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {
			Actions: model.Grants{
				Users: map[string]api.Actions{"alice": api.ActionRead | api.ActionWrite},
			},
			Targets: map[string]model.Target{
				api.TargetAnyLocal: {Include: []string{"**"}, Exclude: []string{"*.tmp"}},
			},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[api.ResourceType]model.Resource
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Error(diff)
	}
	p := model.Permission{
		Resources: map[api.ResourceType]model.Resource{
			api.ResourceBuild:    {},
			api.ResourceArtifact: {},
		},
	}
	if diff := cmp.Diff(
		[]api.ResourceType{api.ResourceArtifact, api.ResourceBuild},
		p.ResourceTypes(),
	); diff != "" {
		t.Error(diff)
	}
}

func TestUnitMatchesRepository(t *testing.T) {
	cases := []struct {
		target, key, class string
		want               bool
	}{
		{api.TargetAny, "x", "remote", true},
		{api.TargetAnyLocal, "x", "local", true},
		{api.TargetAnyLocal, "x", "federated", true},
		{api.TargetAnyLocal, "x", "remote", false},
		{api.TargetAnyRemote, "x", "remote", true},
		{api.TargetAnyRemote, "x", "virtual", false},
		{api.TargetAnyDistribution, "x", "distribution", true},
		{"libs", "libs", "local", true},
		{"libs", "other", "local", false},
	}
	for _, c := range cases {
		if got := model.MatchesRepository(c.target, c.key, c.class); got != c.want {
			t.Errorf("%q %q %q = %v", c.target, c.key, c.class, got)
		}
	}
}

func TestUnitPermissionV1RoundTrip(t *testing.T) {
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
		p, err := model.PermissionFromV1(in)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		out, err := model.PermissionToV1(p)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if diff := cmp.Diff(in, out); diff != "" {
			t.Errorf("%s round trip (-in +out):\n%s", in.Name, diff)
		}
	}
	// An omitted include pattern means "**".
	p, err := model.PermissionFromV1(
		wire.PermissionTargetV1{Name: "d", Repositories: []string{"r"}},
	)
	if err != nil ||
		!cmp.Equal(p.Resources[api.ResourceArtifact].Targets["r"].Include, []string{"**"}) {
		t.Errorf("default include: %+v %v", p, err)
	}
	if _, err := model.PermissionFromV1(wire.PermissionTargetV1{
		Principals: wire.PrincipalsV1{Groups: map[string][]string{"g": {"zz"}}},
	}); !errors.Is(err, api.ErrUnknownAction) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

func TestUnitPermissionV2RoundTrip(t *testing.T) {
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
			Repositories:    []string{"release-bundles"},
			Actions:         wire.ActionsV2{Users: map[string][]string{"rel": {"distribute"}}},
		},
	}
	p, err := model.PermissionFromV2(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := model.PermissionToV2(p)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	// The legacy API emits [""] to mean "no exclusions".
	p, err = model.PermissionFromV2(wire.PermissionTargetV2{Name: "x", Repo: &wire.SectionV2{
		IncludePatterns: []string{""}, ExcludePatterns: []string{""}, Repositories: []string{"r"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Resources[api.ResourceArtifact].Targets["r"]; !cmp.Equal(
		got,
		model.Target{Include: []string{"**"}},
	) {
		t.Errorf("empty patterns: %+v", got)
	}
	if _, err := model.PermissionFromV2(wire.PermissionTargetV2{Build: &wire.SectionV2{
		Actions: wire.ActionsV2{Users: map[string][]string{"u": {"r"}}},
	}}); !errors.Is(err, api.ErrUnknownAction) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

// TestUnitPermissionV2SectionDefaults checks a v2 build or release bundle
// section sent without repositories targets its default repository, and
// a repository section targets nothing (createorreplacepermissiontargetv2).
func TestUnitPermissionV2SectionDefaults(t *testing.T) {
	for _, repos := range [][]string{nil, {}} {
		p, err := model.PermissionFromV2(wire.PermissionTargetV2{
			Name:          "defaults",
			Repo:          &wire.SectionV2{Repositories: repos},
			Build:         &wire.SectionV2{Repositories: repos},
			ReleaseBundle: &wire.SectionV2{Repositories: repos},
		})
		if err != nil {
			t.Fatal(err)
		}
		everything := model.Target{Include: []string{"**"}}
		for rt, want := range map[api.ResourceType]map[string]model.Target{
			api.ResourceArtifact:      {},
			api.ResourceBuild:         {"artifactory-build-info": everything},
			api.ResourceReleaseBundle: {"release-bundles": everything},
		} {
			if got := p.Resources[rt].Targets; !cmp.Equal(got, want) {
				t.Errorf("%s with repositories %#v: %+v, want %+v", rt, repos, got, want)
			}
		}
	}
	// Repositories that are sent are kept, and validation judges them.
	p, err := model.PermissionFromV2(wire.PermissionTargetV2{
		Name:          "named",
		ReleaseBundle: &wire.SectionV2{Repositories: []string{"rb-source"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Resources[api.ResourceReleaseBundle].Targets["rb-source"]; !ok {
		t.Errorf("named source repository dropped: %+v", p.Resources)
	}
}

func TestUnitLegacyProjectionsRefuseMixedPatterns(t *testing.T) {
	p := model.Permission{Name: "mixed", Resources: map[api.ResourceType]model.Resource{
		api.ResourceArtifact: {Targets: map[string]model.Target{
			"a": {Include: []string{"**"}},
			"b": {Include: []string{"org/**"}},
		}},
	}}
	if _, err := model.PermissionToV1(p); !errors.Is(err, model.ErrLossyProjection) {
		t.Errorf("v1: want ErrLossyProjection, got %v", err)
	}
	if _, err := model.PermissionToV2(p); !errors.Is(err, model.ErrLossyProjection) {
		t.Errorf("v2: want ErrLossyProjection, got %v", err)
	}
	// Non-repository sections are simply absent from the v1 view.
	p = model.Permission{
		Name:      "b",
		Resources: map[api.ResourceType]model.Resource{api.ResourceBuild: {}},
	}
	v1, err := model.PermissionToV1(p)
	if err != nil || len(v1.Repositories) != 0 || v1.IncludesPattern != "**" {
		t.Errorf("%+v %v", v1, err)
	}
}

func TestUnitPermissionAccessRoundTrip(t *testing.T) {
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
	p, err := model.PermissionFromAccess(in)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, model.PermissionToAccess(p)); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	if _, err := model.PermissionFromAccess(
		wire.AccessPermission{Resources: map[string]wire.AccessResource{
			"artifact": {Actions: wire.AccessActions{Users: map[string][]string{"u": {"FLY"}}}},
		}},
	); !errors.Is(
		err,
		api.ErrUnknownAction,
	) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

// One internal permission reads the same through all three wire forms when
// it is representable in all of them.
func TestUnitPermissionAcrossWireForms(t *testing.T) {
	v1 := wire.PermissionTargetV1{
		Name:            "p",
		IncludesPattern: "**",
		Repositories:    []string{"ANY"},
		Principals:      wire.PrincipalsV1{Users: map[string][]string{"u": {"r", "w"}}},
	}
	p1, err := model.PermissionFromV1(v1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := model.PermissionFromAccess(model.PermissionToAccess(p1))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := model.PermissionToV2(p2)
	if err != nil {
		t.Fatal(err)
	}
	p3, err := model.PermissionFromV2(v2)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(p1, p3); diff != "" {
		t.Errorf("(-v1 +v2):\n%s", diff)
	}
}

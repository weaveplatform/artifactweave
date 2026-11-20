package api_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitCapabilityProjectScope(t *testing.T) {
	p := kernel.Principal{
		Name: "publisher", IsTokenAuthenticated: true, TokenProject: "alpha",
		ProjectIncarnations: map[string]string{"alpha": "original"},
	}
	ceiling := api.ProjectScopeOf(p)
	p.ProjectIncarnations["alpha"] = "changed"
	raw, err := json.Marshal(ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"project":"alpha","project_incarnations":{"alpha":"original"}}` {
		t.Fatalf("signed capability project fields changed: %s", raw)
	}
	var restored api.ProjectScope
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	scope := kernel.UserScope()
	subject := (api.Subject{Name: "publisher", Groups: []string{"writers"}, Scope: &scope}).WithProjectScope(restored)
	restored.Lifetimes["alpha"] = "recreated"
	if subject.ProjectKey != "alpha" || subject.ProjectIncarnations["alpha"] != "original" ||
		subject.Name != "publisher" || !reflect.DeepEqual(subject.Groups, []string{"writers"}) || subject.Scope != &scope {
		t.Fatalf("capability must preserve independent project and existing resource ceilings: %+v", subject)
	}
	if subject.WithProjectScope(api.ProjectScope{}).ProjectIncarnations != nil ||
		subject.WithProjectScope(api.ProjectScope{}).ProjectKey != "" {
		t.Fatal("an unbound capability inherited another project ceiling")
	}
	for _, candidate := range []kernel.Principal{
		{Name: "publisher", Password: true, TokenProject: "alpha", ProjectIncarnations: p.ProjectIncarnations},
		{Name: "publisher", TokenProject: "alpha", ProjectIncarnations: p.ProjectIncarnations},
		{Name: "publisher", IsTokenAuthenticated: true},
	} {
		raw, err := json.Marshal(api.ProjectScopeOf(candidate))
		if err != nil || string(raw) != `{}` {
			t.Fatalf("unbound or unsigned caller gained a project ceiling: %s, %v", raw, err)
		}
	}
}

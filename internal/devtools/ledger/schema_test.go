package ledger

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestUnitSchemaAgreesWithValidator(t *testing.T) {
	var schema map[string]any
	raw := repopath.Read(t, "migration/ledger.schema.json")
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	at := func(keys ...string) any {
		var v any = schema
		for _, k := range keys {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("schema has no object at %s", strings.Join(keys, "."))
			}
			v = m[k]
		}
		return v
	}
	strs := func(values []string) []any {
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	}

	for _, c := range []struct {
		path string
		want any
	}{
		{"properties.schema.const", float64(schemaVersion)},
		{"properties.module.const", module},
		{"properties.staging_root.const", stagingRoot},
		{"properties.final_root.const", finalRoot},
		{"$defs.legacy.pattern", legacyPattern},
		{"$defs.legacyFile.pattern", legacyFilePattern},
		{"$defs.target.pattern", targetPattern},
		{"$defs.ticket.pattern", ticketPattern},
		{"$defs.phase.pattern", phasePattern},
		{"$defs.scope.enum", strs(scopes)},
		{"$defs.package.properties.action.enum", strs(packageActions)},
		{"$defs.package.properties.name.pattern", namePattern},
		{"$defs.package.properties.rename.propertyNames.pattern", fileNamePattern},
		{"$defs.package.properties.rename.additionalProperties.pattern", fileNamePattern},
		{"$defs.file.properties.action.enum", strs(fileActions)},
		{"$defs.file.properties.file.pattern", fileNamePattern},
		{"$defs.file.properties.targets.minItems", float64(minTargets)},
		{"$defs.placeholder.properties.phases.minItems", float64(minPhases)},
	} {
		if got := at(strings.Split(c.path, ".")...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("schema %s = %v; the Go validator uses %v", c.path, got, c.want)
		}
	}

	// The schema names exactly the JSON fields of the Go types, so neither
	// can gain a field the other does not know.
	for _, c := range []struct {
		path string
		typ  reflect.Type
	}{
		{"properties", reflect.TypeFor[Ledger]()},
		{"$defs.package.properties", reflect.TypeFor[Package]()},
		{"$defs.file.properties", reflect.TypeFor[File]()},
		{"$defs.placeholder.properties", reflect.TypeFor[Placeholder]()},
	} {
		props, ok := at(strings.Split(c.path, ".")...).(map[string]any)
		if !ok {
			t.Fatalf("schema has no object at %s", c.path)
		}
		var fields []string
		for i := range c.typ.NumField() {
			fields = append(fields, strings.Split(c.typ.Field(i).Tag.Get("json"), ",")[0])
		}
		got, want := slices.Sorted(maps.Keys(props)), slices.Sorted(slices.Values(fields))
		if !slices.Equal(got, want) {
			t.Errorf("schema %s names %v; %s has JSON fields %v", c.path, got, c.typ.Name(), want)
		}
	}
}

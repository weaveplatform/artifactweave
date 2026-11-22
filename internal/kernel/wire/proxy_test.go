package wire_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
)

func TestUnitProxyConfigurationServices(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        []string
		invalid     bool
	}{
		{name: "absent"},
		{name: "null", field: `,"services":null`},
		{name: "empty string", field: `,"services":""`},
		{name: "empty list", field: `,"services":[]`},
		{name: "provider commas", field: `,"services":"jfrt,jfxr"`, want: []string{"jfrt", "jfxr"}},
		{name: "source separators", field: `,"services":" ,jfxr; jfrt\njfrt,,; "`, want: []string{"jfxr", "jfrt", "jfrt"}},
		{name: "only separators", field: `,"services":" ,;\n "`},
		{name: "other whitespace", field: `,"services":"jfrt\tjfxr"`, want: []string{"jfrt\tjfxr"}},
		{name: "historical list", field: `,"services":["jfrt","jfxr"]`, want: []string{"jfrt", "jfxr"}},
		{name: "list entries unchanged", field: `,"services":[" jfrt ","jfrt,jfxr",""]`, want: []string{" jfrt ", "jfrt,jfxr", ""}},
		{name: "number", field: `,"services":42`, invalid: true},
		{name: "boolean", field: `,"services":true`, invalid: true},
		{name: "object", field: `,"services":{"secret":"private-value"}`, invalid: true},
		{name: "mixed list", field: `,"services":["private-value",42]`, invalid: true},
		{name: "null list entry", field: `,"services":[null]`, invalid: true},
		{name: "nested list", field: `,"services":[["private-value"]]`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reusing a value must not keep services from its previous document.
			got := wire.ProxyConfiguration{Services: []string{"previous"}}
			err := json.Unmarshal([]byte(`{"host":"proxy.invalid","port":3128`+tc.field+`}`), &got)
			if tc.invalid {
				if err == nil || strings.Contains(err.Error(), "private-value") {
					t.Fatal("invalid services admitted or disclosed")
				}
				return
			}
			if err != nil || got.Host != "proxy.invalid" || got.Port != 3128 || !slices.Equal(got.Services, tc.want) {
				t.Fatalf("decoded proxy = %#v, %v", got, err)
			}
		})
	}
	var got wire.ProxyConfiguration
	if err := json.Unmarshal([]byte(`{"host":{"secret":"private-value"}}`), &got); err == nil ||
		strings.Contains(err.Error(), "private-value") {
		t.Fatal("invalid proxy field admitted or disclosed")
	}
}

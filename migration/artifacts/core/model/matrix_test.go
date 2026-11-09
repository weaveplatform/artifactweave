package model_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
)

func TestUnitMatrixMatches(t *testing.T) {
	props := model.Properties{"os": {"linux"}, "arch": {"amd64", "arm64"}}
	for _, tc := range []struct {
		name string
		m    model.Matrix
		want bool
	}{
		{"no parameters", model.Matrix{}, true},
		{"a carried value", model.Matrix{Props: model.Properties{"os": {"linux"}}}, true},
		{"one of the values", model.Matrix{Props: model.Properties{"arch": {"arm64"}}}, true},
		{"one of the asked", model.Matrix{Props: model.Properties{"os": {"mac", "linux"}}}, true},
		{"another value", model.Matrix{Props: model.Properties{"os": {"mac"}}}, false},
		{"a key not carried", model.Matrix{Props: model.Properties{"tier": {"gold"}}}, true},
		{
			"a mandatory key not carried",
			model.Matrix{Props: model.Properties{"tier": {"gold"}}, Mandatory: map[string]bool{"tier": true}},
			false,
		},
		{
			"a mandatory key carried",
			model.Matrix{Props: model.Properties{"os": {"linux"}}, Mandatory: map[string]bool{"os": true}},
			true,
		},
		{
			"one key of two fails",
			model.Matrix{Props: model.Properties{"os": {"linux"}, "arch": {"386"}}},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Matches(props); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

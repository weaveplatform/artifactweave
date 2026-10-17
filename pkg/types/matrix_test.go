package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestMatrixMatches(t *testing.T) {
	props := types.Properties{"os": {"linux"}, "arch": {"amd64", "arm64"}}
	for _, tc := range []struct {
		name string
		m    types.Matrix
		want bool
	}{
		{"no parameters", types.Matrix{}, true},
		{"a carried value", types.Matrix{Props: types.Properties{"os": {"linux"}}}, true},
		{"one of the values", types.Matrix{Props: types.Properties{"arch": {"arm64"}}}, true},
		{"one of the asked", types.Matrix{Props: types.Properties{"os": {"mac", "linux"}}}, true},
		{"another value", types.Matrix{Props: types.Properties{"os": {"mac"}}}, false},
		{"a key not carried", types.Matrix{Props: types.Properties{"tier": {"gold"}}}, true},
		{
			"a mandatory key not carried",
			types.Matrix{Props: types.Properties{"tier": {"gold"}}, Mandatory: map[string]bool{"tier": true}},
			false,
		},
		{
			"a mandatory key carried",
			types.Matrix{Props: types.Properties{"os": {"linux"}}, Mandatory: map[string]bool{"os": true}},
			true,
		},
		{
			"one key of two fails",
			types.Matrix{Props: types.Properties{"os": {"linux"}, "arch": {"386"}}},
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

package api_test

import (
	"encoding/json"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
)

func TestUnitIsNull(t *testing.T) {
	for raw, want := range map[string]bool{
		`null`:        true,
		" null\n\t":   true,
		`"null"`:      false,
		`nul`:         false,
		``:            false,
		`{}`:          false,
		`[null]`:      false,
		`"value"`:     false,
		`false`:       false,
		"null null\n": false,
	} {
		if got := artifactsapi.IsNull(json.RawMessage(raw)); got != want {
			t.Errorf("IsNull(%q) = %v, want %v", raw, got, want)
		}
	}
}

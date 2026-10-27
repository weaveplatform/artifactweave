package golang_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/golang"
)

// A .info deployed through the Go API must be one the go command can read:
// @latest and a stored .info serve it as it is.
func TestUnitCheckInfo(t *testing.T) {
	f := golang.File{Module: "example.com/m", Version: "v1.2.0+incompatible", Ext: golang.ExtInfo}
	for body, why := range map[string]string{
		// jf go-publish writes local time followed by Z (research 26 3.1).
		`{"Version":"v1.2.0+incompatible","Time":"2026-10-17T21:04:05Z"}`:      "",
		`{"Version":"v1.2.0+incompatible","Time":"2026-10-17T21:04:05+01:00"}`: "",
		`{"Version":"v1.2.0+incompatible"}`:                                    "",
		`{"Version":"v1.2.0+incompatible","Time":null}`:                        "",
		`{"Version":"v1.2.0+incompatible","Origin":{"VCS":"git"}}`:             "",
		`[]`:                                   "is not a JSON object",
		`null`:                                 "is not a JSON object",
		`{"Version":"v1.2.0+incompatible"} {}`: "is not a JSON object",
		`{}`:                                   "does not give its Version as v1.2.0+incompatible",
		`{"Version":"v1.2.0"}`:                 "does not give its Version as v1.2.0+incompatible",
		`{"Version":1}`:                        "does not give its Version as v1.2.0+incompatible",
		`{"version":"v1.2.0+incompatible"}`:    "does not give its Version as v1.2.0+incompatible",
		`{"Version":"v1.2.0+incompatible","Time":7}`:                     "gives a Time that is not a string",
		`{"Version":"v1.2.0+incompatible","Time":"2026-10-17 21:04:05"}`: "gives a Time that is not RFC 3339",
	} {
		err := golang.CheckInfo(f, []byte(body))
		var invalid *golang.InvalidPathError
		switch {
		case why == "" && err != nil:
			t.Errorf("%s: %v", body, err)
		case why == "":
		case !errors.As(err, &invalid):
			t.Errorf("%s: %v, want an *InvalidPathError", body, err)
		case invalid.Message != "The .info of version v1.2.0+incompatible of module example.com/m "+why+".":
			t.Errorf("%s: %q", body, invalid.Message)
		}
	}
	big := `{"Version":"v1.2.0+incompatible","Pad":"` + strings.Repeat("x", golang.MaxInfo) + `"}`
	if err := golang.CheckInfo(f, []byte(big)); err == nil ||
		!strings.Contains(err.Error(), "larger than 64 KiB") {
		t.Errorf("a .info over 64 KiB: %v", err)
	}
}

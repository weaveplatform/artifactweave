package api_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
)

func TestUnitIsReferenceToken(t *testing.T) {
	ref := "cmVmd" + strings.Repeat("A", 59)
	if !api.IsReferenceToken(ref) || api.IsReferenceToken(ref+"A") ||
		api.IsReferenceToken("eyJhbGciOi") {
		t.Error("IsReferenceToken")
	}
}

func TestUnitLooksLikeToken(t *testing.T) {
	for s, want := range map[string]bool{
		"eyJhbGciOi.eyJzdWIi.c2ln": true,
		// One character short of a reference token.
		"cmVmd0123456789012345678901234567890123456789012345678901234567": false,
		"password":    false,
		"eyJonly.two": false,
		"a.b.c":       false,
	} {
		if got := api.LooksLikeToken(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
	ref := "cmVmd" + "01234567890123456789012345678901234567890123456789012345678"
	if !api.LooksLikeToken(ref) {
		t.Errorf("reference %q", ref)
	}
}

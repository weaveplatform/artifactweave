package api_test

import (
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

func TestUnitAPIKey(t *testing.T) {
	type want struct {
		key      string
		ok, sent bool
	}
	for name, c := range map[string]struct {
		h    http.Header
		want want
	}{
		"none":                 {http.Header{}, want{"", false, false}},
		"X-JFrog-Art-Api":      {http.Header{"X-Jfrog-Art-Api": {"k1"}}, want{"k1", true, true}},
		"first line only":      {http.Header{"X-Jfrog-Art-Api": {"k1", "k2"}}, want{"k1", true, true}},
		"X-Api-Key":            {http.Header{"X-Api-Key": {"k2"}}, want{"k2", true, true}},
		"X-JFrog-Art-Api wins": {http.Header{"X-Jfrog-Art-Api": {"k1"}, "X-Api-Key": {"k2"}}, want{"k1", true, true}},
		"a blank one gives way": {
			http.Header{"X-Jfrog-Art-Api": {" \t"}, "X-Api-Key": {"k2"}}, want{"k2", true, true},
		},
		"X-JFrog-Art-Api blank alone": {http.Header{"X-Jfrog-Art-Api": {" "}}, want{"", false, false}},
		// X-Api-Key counts whenever it is sent, but only one that is not
		// blank turns a refusal into 403.
		"X-Api-Key empty":      {http.Header{"X-Api-Key": {""}}, want{"", true, false}},
		"X-Api-Key blank":      {http.Header{"X-Api-Key": {" "}}, want{" ", true, false}},
		"both blank":           {http.Header{"X-Jfrog-Art-Api": {""}, "X-Api-Key": {" "}}, want{" ", true, false}},
		"a non-breaking space": {http.Header{"X-Jfrog-Art-Api": {" "}}, want{" ", true, true}},
	} {
		key, ok := api.APIKey(c.h)
		if got := (want{key, ok, api.APIKeySent(c.h)}); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

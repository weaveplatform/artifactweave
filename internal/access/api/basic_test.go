package api_test

import (
	"encoding/base64"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
)

func TestUnitBasicCredentials(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	type want struct {
		ignored      bool
		ok           bool
		user, secret string
	}
	for value, w := range map[string]want{
		"Basic " + enc("ann:pw"): {ok: true, user: "ann", secret: "pw"},
		// The secret keeps every colon after the first.
		"Basic " + enc("ann:p:w"): {ok: true, user: "ann", secret: "p:w"},
		// A Basic value 7.x reads but which does not verify: an empty user
		// name with a password, a value without a colon, one that does not
		// decode, and one with a second space before it.
		"Basic " + enc(":pw"):      {ok: true, secret: "pw"},
		"Basic " + enc("ann"):      {},
		"Basic !!!":                {},
		"Basic  " + enc("ann:pw"):  {},
		"Basic Og==":               {ignored: true},
		"Basic":                    {ignored: true},
		"Basic    ":                {ignored: true},
		"basic " + enc("ann:pw"):   {ignored: true},
		"BASIC " + enc("ann:pw"):   {ignored: true},
		"basic":                    {ignored: true},
		"Basic\t" + enc("ann:pw"):  {ignored: true},
		"Bearer " + enc("ann:pw"):  {},
		"Basically " + enc("a:b"):  {},
		"":                         {},
		enc("ann:pw"):              {},
		"Digest username=\"ann\"":  {},
		"bearer eyJhbGciOi.e30.c2": {},
	} {
		if got := api.IgnoredAuthorization(value); got != w.ignored {
			t.Errorf("IgnoredAuthorization(%q) = %v, want %v", value, got, w.ignored)
		}
		user, secret, ok := api.BasicCredentials(value)
		if ok != w.ok || user != w.user || secret != w.secret {
			t.Errorf("BasicCredentials(%q) = %q, %q, %v; want %q, %q, %v",
				value, user, secret, ok, w.user, w.secret, w.ok)
		}
	}
}

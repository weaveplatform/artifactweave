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
		"Basic " + enc(":pw"):     {ok: true, secret: "pw"},
		"Basic " + enc("ann"):     {},
		"Basic !!!":               {},
		"Basic  " + enc("ann:pw"): {},
		"Basic Og==":              {ignored: true},
		"Basic":                   {ignored: true},
		"Basic    ":               {ignored: true},
		// A no-break space is not blank to Java's isBlank: 7.x reads the
		// value as Basic, and it does not decode.
		"Basic \u00a0":             {},
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

func TestUnitBearerToken(t *testing.T) {
	type want struct {
		token string
		ok    bool
	}
	for value, w := range map[string]want{
		"Bearer eyJ.e30.c2":   {"eyJ.e30.c2", true},
		"bearer eyJ.e30.c2":   {"eyJ.e30.c2", true},
		"BEARER eyJ.e30.c2":   {"eyJ.e30.c2", true},
		"Bearer  eyJ.e30.c2 ": {"eyJ.e30.c2", true},
		// The props filter reads "Bearer " exactly in a value of nine
		// units or more, spaces and all; the value does not verify.
		"Bearer   ": {"", true},
		// A no-break space is not blank to Java's isBlank.
		"bearer \u00a0": {"", true},
		// Values 7.x reads no Bearer credential from.
		"Bearer":             {},
		"Bearer ":            {},
		"Bearer  ":           {},
		"bearer    ":         {},
		"BEARER \t ":         {},
		"Bearer\teyJ.e30.c2": {},
		"Bearereyj":          {},
		"Basic Og==":         {},
		"Negotiate abc":      {},
		"Token abc":          {},
		"":                   {},
	} {
		token, ok := api.BearerToken(value)
		if token != w.token || ok != w.ok {
			t.Errorf("BearerToken(%q) = %q, %v; want %q, %v", value, token, ok, w.token, w.ok)
		}
	}
}

func TestUnitBlank(t *testing.T) {
	for s, want := range map[string]bool{
		"": true, " \t\n\r\f\v": true, "\u2003\u2028\u2029\u001c": true,
		"\u00a0": false, "\u2007": false, "\u202f": false, " a ": false,
	} {
		if got := api.Blank(s); got != want {
			t.Errorf("Blank(%q) = %v, want %v", s, got, want)
		}
	}
}

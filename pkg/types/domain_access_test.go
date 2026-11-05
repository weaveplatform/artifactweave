package types_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitToken(t *testing.T) {
	tok := types.Token{ExpiresAt: t0}
	if tok.Expired(t0.Add(-time.Second)) || !tok.Expired(t0) ||
		(types.Token{}).Expired(t0.AddDate(100, 0, 0)) {
		t.Error("Expired")
	}
	cases := []struct {
		in                 time.Duration
		force, refreshable bool
		want               bool
	}{
		{0, false, false, true},
		{time.Hour, false, false, false},
		{time.Hour, true, false, true},
		{time.Hour, false, true, true},
		{6 * time.Hour, false, false, true},
	}
	for _, c := range cases {
		got := types.Revocable(c.in, types.DefaultRevocableThreshold, c.force, c.refreshable)
		if got != c.want {
			t.Errorf("Revocable(%v, %v, %v) = %v", c.in, c.force, c.refreshable, got)
		}
	}
	if types.DefaultTokenExpiry != 31536000*time.Second {
		t.Errorf("DefaultTokenExpiry = %v, want one year", types.DefaultTokenExpiry)
	}
	s, err := types.ParseSubject("jfac@01abc/users/bob")
	if err != nil || s != (types.Subject{ServiceID: "jfac@01abc", Kind: "users", Name: "bob"}) ||
		s.String() != "jfac@01abc/users/bob" {
		t.Errorf("%+v %v", s, err)
	}
	for _, bad := range []string{"bob", "jfac@x", "noat/users/bob", "jfac@x/users", "jfac@x//bob"} {
		if _, err := types.ParseSubject(bad); !errors.Is(err, types.ErrInvalidSubject) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	ref := "cmVmd" + strings.Repeat("A", 59)
	if !types.IsReferenceToken(ref) || types.IsReferenceToken(ref+"A") ||
		types.IsReferenceToken("eyJhbGciOi") {
		t.Error("IsReferenceToken")
	}
}

func TestUnitProject(t *testing.T) {
	for _, ok := range []string{"ab", "my-proj", "a" + strings.Repeat("b", 31)} {
		if err := types.ValidateProjectKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a", "1ab", "Ab", "a_b", "a" + strings.Repeat("b", 32)} {
		if err := types.ValidateProjectKey(bad); !errors.Is(err, types.ErrInvalidProjectKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if types.ProjectRepoKey("p", "docker") != "p-docker" ||
		types.ProjectRepoKey("p", "p-docker") != "p-docker" {
		t.Error("ProjectRepoKey")
	}
	if !(types.Role{Type: types.RoleCustom}).Editable() ||
		(types.Role{Type: types.RolePredefined}).Editable() {
		t.Error("Role.Editable")
	}
}

func TestUnitSigningKeyJSON(t *testing.T) {
	raw, err := json.Marshal(types.SigningKey{Alias: "k", PrivateKey: "secret", Passphrase: "pass"})
	if err != nil || strings.Contains(string(raw), "secret") ||
		strings.Contains(string(raw), "pass") {
		t.Errorf("signing key secrets leaked: %s %v", raw, err)
	}
}

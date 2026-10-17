package types_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/pkg/types/wire"
)

func b(v bool) *bool { return &v }

func TestUserLegacyRoundTrip(t *testing.T) {
	in := types.User{
		Name:             "davids",
		Email:            "davids@example.com",
		Admin:            true,
		ProfileUpdatable: true,
		Realm:            "ldap",
		Status:           types.UserLocked,
		MFA:              types.MFAEnabled,
		Groups:           []string{"readers"},
		LastLoggedIn:     time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC),
		Roles: types.RoleFlags{
			PolicyViewer:   true,
			WatchManager:   true,
			ReportsManager: true,
			PolicyManager:  true,
		},
	}
	w := types.UserToLegacy(in)
	if w.LastLoggedIn != "2026-01-02T03:04:05.006Z" || w.Status != "LOCKED" {
		t.Fatalf("legacy rendering: %+v", w)
	}
	out, err := types.UserFromLegacy(w)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
}

func TestUserFromLegacyDefaults(t *testing.T) {
	u, err := types.UserFromLegacy(wire.LegacyUser{Name: "n", Email: "e", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	want := types.User{
		Name: "n", Email: "e", ProfileUpdatable: true, Realm: types.RealmInternal,
		Status: types.UserEnabled, MFA: types.MFANone,
	}
	if diff := cmp.Diff(want, u); diff != "" {
		t.Error(diff)
	}
	if _, err := types.UserFromLegacy(
		wire.LegacyUser{LastLoggedIn: "yesterday"},
	); !errors.Is(
		err,
		types.ErrInvalidTimestamp,
	) {
		t.Errorf("want ErrInvalidTimestamp, got %v", err)
	}
	if _, err := types.UserFromLegacy(
		wire.LegacyUser{Status: "ASLEEP"},
	); !errors.Is(
		err,
		types.ErrInvalidStatus,
	) {
		t.Errorf("want ErrInvalidStatus, got %v", err)
	}
}

func TestLegacyStatusMapping(t *testing.T) {
	for status, want := range map[types.UserStatus]string{
		types.UserEnabled: "ENABLED", types.UserDisabled: "DISABLED", types.UserLocked: "LOCKED",
		types.UserInvited: "DISABLED", "": "",
	} {
		if got := types.UserToLegacy(types.User{Status: status}).Status; got != want {
			t.Errorf("%q -> %q, want %q", status, got, want)
		}
	}
}

func TestUserAccessRoundTrip(t *testing.T) {
	in := types.User{
		Name:                     "alice",
		Email:                    "a@example.com",
		InternalPasswordDisabled: true,
		DisableUIAccess:          true,
		Realm:                    types.RealmInternal,
		Status:                   types.UserInvited,
		MFA:                      types.MFANone,
		Groups:                   []string{"a", "b"},
		LastLoggedIn:             time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC),
		Roles: types.RoleFlags{
			WatchManager: true, PolicyViewer: true, PolicyManager: true, ReportsManager: true,
			ResourcesManager: true, ManageWebhook: true, PlatformAuditor: true,
		},
	}
	w := types.UserToAccess(in, true)
	if w.EffectiveAdmin == nil || !*w.EffectiveAdmin || w.Status != "invited" {
		t.Fatalf("access rendering: %+v", w)
	}
	out, err := types.UserFromAccess(w)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	bad := "x"
	if _, err := types.UserFromAccess(
		wire.AccessUser{LastLoggedIn: &bad},
	); !errors.Is(
		err,
		types.ErrInvalidTimestamp,
	) {
		t.Errorf("want ErrInvalidTimestamp, got %v", err)
	}
	if _, err := types.UserFromAccess(
		wire.AccessUser{Status: "gone"},
	); !errors.Is(
		err,
		types.ErrInvalidStatus,
	) {
		t.Errorf("want ErrInvalidStatus, got %v", err)
	}
}

func TestGroupRoundTrips(t *testing.T) {
	legacy := wire.LegacyGroup{
		Name: "readers", Description: "d", AutoJoin: b(true), AdminPrivileges: b(false),
		Realm: "ldap", RealmAttributes: "dn=x", UserNames: []string{"u1"},
		WatchManager: b(true), PolicyManager: b(false), ReportsManager: b(true),
	}
	if diff := cmp.Diff(legacy, types.GroupToLegacy(types.GroupFromLegacy(legacy))); diff != "" {
		t.Errorf("legacy (-in +out):\n%s", diff)
	}
	access := wire.AccessGroup{
		Name:            "devs",
		Description:     "d",
		AutoJoin:        b(false),
		AdminPrivileges: b(true),
		ExternalID:      "ext",
		Members: []string{
			"a",
		},
		ReportsManager:  b(true),
		WatchManager:    b(false),
		PolicyManager:   b(true),
		PolicyViewer:    b(true),
		ManageResources: b(true),
		ManageWebhook:   b(false),
		Realm:           "internal",
	}
	if diff := cmp.Diff(access, types.GroupToAccess(types.GroupFromAccess(access))); diff != "" {
		t.Errorf("access (-in +out):\n%s", diff)
	}
	if g := types.GroupFromAccess(wire.AccessGroup{Name: "x"}); g.Realm != types.RealmInternal {
		t.Errorf("default realm: %q", g.Realm)
	}
}

func TestEffectiveAdmin(t *testing.T) {
	groups := []types.Group{{Name: "admins", AdminPrivileges: true}, {Name: "readers"}}
	cases := []struct {
		u    types.User
		want bool
	}{
		{types.User{Admin: true}, true},
		{types.User{Groups: []string{"admins"}}, true},
		{types.User{Groups: []string{"readers"}}, false},
		{types.User{}, false},
	}
	for _, c := range cases {
		if got := types.EffectiveAdmin(c.u, groups); got != c.want {
			t.Errorf("%+v = %v", c.u, got)
		}
	}
}

// A user who never logged in carries "last_logged_in": null in the Access v2
// shape, as Artifactory answers since 7.98.7, and reads back as never.
func TestUserAccessNeverLoggedIn(t *testing.T) {
	w := types.UserToAccess(types.User{Name: "bob", Status: types.UserEnabled}, false)
	if w.LastLoggedIn != nil {
		t.Fatalf("last_logged_in: %q", *w.LastLoggedIn)
	}
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if v, ok := doc["last_logged_in"]; !ok || v != nil {
		t.Errorf("last_logged_in in %s", raw)
	}
	u, err := types.UserFromAccess(w)
	if err != nil || !u.LastLoggedIn.IsZero() {
		t.Errorf("read back: %v %v", u.LastLoggedIn, err)
	}
}

package model_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel/wire"
)

func b(v bool) *bool { return &v }

func TestUnitUserLegacyRoundTrip(t *testing.T) {
	in := model.User{
		Name:             "davids",
		Email:            "davids@example.com",
		Admin:            true,
		ProfileUpdatable: true,
		Realm:            "ldap",
		Status:           model.UserLocked,
		MFA:              model.MFAEnabled,
		Groups:           []string{"readers"},
		LastLoggedIn:     time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC),
		Roles: model.RoleFlags{
			PolicyViewer:   true,
			WatchManager:   true,
			ReportsManager: true,
			PolicyManager:  true,
		},
	}
	w := model.UserToLegacy(in)
	if w.LastLoggedIn != "2026-01-02T03:04:05.006Z" || w.Status != "LOCKED" {
		t.Fatalf("legacy rendering: %+v", w)
	}
	out, err := model.UserFromLegacy(w)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
}

func TestUnitUserFromLegacyDefaults(t *testing.T) {
	u, err := model.UserFromLegacy(wire.LegacyUser{Name: "n", Email: "e", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	want := model.User{
		Name: "n", Email: "e", ProfileUpdatable: true, Realm: model.RealmInternal,
		Status: model.UserEnabled, MFA: model.MFANone,
	}
	if diff := cmp.Diff(want, u); diff != "" {
		t.Error(diff)
	}
	if _, err := model.UserFromLegacy(
		wire.LegacyUser{LastLoggedIn: "yesterday"},
	); !errors.Is(
		err,
		model.ErrInvalidTimestamp,
	) {
		t.Errorf("want ErrInvalidTimestamp, got %v", err)
	}
	if _, err := model.UserFromLegacy(
		wire.LegacyUser{Status: "ASLEEP"},
	); !errors.Is(
		err,
		model.ErrInvalidStatus,
	) {
		t.Errorf("want ErrInvalidStatus, got %v", err)
	}
}

func TestUnitLegacyStatusMapping(t *testing.T) {
	for status, want := range map[model.UserStatus]string{
		model.UserEnabled: "ENABLED", model.UserDisabled: "DISABLED", model.UserLocked: "LOCKED",
		model.UserInvited: "DISABLED", "": "",
	} {
		if got := model.UserToLegacy(model.User{Status: status}).Status; got != want {
			t.Errorf("%q -> %q, want %q", status, got, want)
		}
	}
}

func TestUnitUserAccessRoundTrip(t *testing.T) {
	in := model.User{
		Name:                     "alice",
		Email:                    "a@example.com",
		InternalPasswordDisabled: true,
		DisableUIAccess:          true,
		Realm:                    model.RealmInternal,
		Status:                   model.UserInvited,
		MFA:                      model.MFANone,
		Groups:                   []string{"a", "b"},
		LastLoggedIn:             time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC),
		Roles: model.RoleFlags{
			WatchManager: true, PolicyViewer: true, PolicyManager: true, ReportsManager: true,
			ResourcesManager: true, ManageWebhook: true, PlatformAuditor: true,
		},
	}
	w := model.UserToAccess(in, true)
	if w.EffectiveAdmin == nil || !*w.EffectiveAdmin || w.Status != "invited" {
		t.Fatalf("access rendering: %+v", w)
	}
	out, err := model.UserFromAccess(w)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(in, out); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	bad := "x"
	if _, err := model.UserFromAccess(
		wire.AccessUser{LastLoggedIn: &bad},
	); !errors.Is(
		err,
		model.ErrInvalidTimestamp,
	) {
		t.Errorf("want ErrInvalidTimestamp, got %v", err)
	}
	if _, err := model.UserFromAccess(
		wire.AccessUser{Status: "gone"},
	); !errors.Is(
		err,
		model.ErrInvalidStatus,
	) {
		t.Errorf("want ErrInvalidStatus, got %v", err)
	}
}

func TestUnitGroupRoundTrips(t *testing.T) {
	legacy := wire.LegacyGroup{
		Name: "readers", Description: "d", AutoJoin: b(true), AdminPrivileges: b(false),
		Realm: "ldap", RealmAttributes: "dn=x", UserNames: []string{"u1"},
		WatchManager: b(true), PolicyManager: b(false), ReportsManager: b(true),
	}
	if diff := cmp.Diff(legacy, model.GroupToLegacy(model.GroupFromLegacy(legacy))); diff != "" {
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
	if diff := cmp.Diff(access, model.GroupToAccess(model.GroupFromAccess(access))); diff != "" {
		t.Errorf("access (-in +out):\n%s", diff)
	}
	if g := model.GroupFromAccess(wire.AccessGroup{Name: "x"}); g.Realm != model.RealmInternal {
		t.Errorf("default realm: %q", g.Realm)
	}
}

func TestUnitEffectiveAdmin(t *testing.T) {
	groups := []model.Group{{Name: "admins", AdminPrivileges: true}, {Name: "readers"}}
	cases := []struct {
		u    model.User
		want bool
	}{
		{model.User{Admin: true}, true},
		{model.User{Groups: []string{"admins"}}, true},
		{model.User{Groups: []string{"readers"}}, false},
		{model.User{}, false},
	}
	for _, c := range cases {
		if got := model.EffectiveAdmin(c.u, groups); got != c.want {
			t.Errorf("%+v = %v", c.u, got)
		}
	}
}

// A user who never logged in carries "last_logged_in": null in the Access v2
// shape, as Artifactory answers since 7.98.7, and reads back as never.
func TestUnitUserAccessNeverLoggedIn(t *testing.T) {
	w := model.UserToAccess(model.User{Name: "bob", Status: model.UserEnabled}, false)
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
	u, err := model.UserFromAccess(w)
	if err != nil || !u.LastLoggedIn.IsZero() {
		t.Errorf("read back: %v %v", u.LastLoggedIn, err)
	}
}

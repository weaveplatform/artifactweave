package server

import (
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// uncomparable is a Grants that == cannot compare, as a fake holding a
// function is; withAccess must test such an Access without comparing it.
type uncomparable struct {
	*accessapitest.Grants
	f func()
}

// TestUnitWithAccess: New and RegisterDebianSnapshotWork begin by binding the
// access module over a Config's store and services when the caller set no
// contract of Access. A caller's Access is kept as it is, and a Config without
// a store keeps a zero one.
func TestUnitWithAccess(t *testing.T) {
	ctx := t.Context()
	empty := withAccess(Config{}).Access
	if !accessUnset(empty) || empty.Settings != (accessapi.Settings{}) {
		t.Errorf("without a store, Access = %+v; want it zero", empty)
	}

	st := inmem.New()
	settings := accessapi.Settings{AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 7}
	bare := withAccess(Config{
		Store: st, AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 7,
	}).Access
	if bare.Authenticator != nil || bare.Tokens != nil {
		t.Errorf("over a store alone, Access = %+v; want no Authenticator or Tokens", bare)
	}
	if bare.Authorizer == nil || bare.AnonymousAccess == nil {
		t.Fatalf("over a store alone, Access = %+v; want its store contracts bound", bare)
	}
	if bare.Settings != settings {
		t.Errorf("Access.Settings = %+v, want %+v from the Config", bare.Settings, settings)
	}
	if err := bare.AnonymousAccess.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	var on bool
	err := st.Settings().Get(ctx, types.SettingAnonymousAccess, &on)
	if err != nil || !on {
		t.Errorf("the Config's store reads anonymous access %v, %v; want on", on, err)
	}

	if err := st.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, st, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewAuthenticator(st.Users(), st.Settings(), auth.Options{
		Hash: testHash, Tokens: tokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	full := withAccess(Config{Store: st, Auth: a, Tokens: tokens}).Access
	v := reflect.ValueOf(full)
	for i := range v.NumField() {
		f, name := v.Field(i), v.Type().Field(i).Name
		if f.Kind() != reflect.Interface {
			continue
		}
		if f.IsNil() {
			t.Errorf("Access.%s is nil with every service set", name)
			continue
		}
		// Each contract on its own counts as set, so withAccess keeps an
		// Access that sets only it.
		var one accessapi.API
		reflect.ValueOf(&one).Elem().Field(i).Set(f)
		if accessUnset(one) {
			t.Errorf("accessUnset ignores %s", name)
		}
		if got := withAccess(Config{Store: st, Access: one}).Access; !reflect.DeepEqual(got, one) {
			t.Errorf("withAccess replaced an Access that sets only %s", name)
		}
	}

	kept := withAccess(Config{Store: st, Access: accessapi.API{
		Grants: uncomparable{Grants: &accessapitest.Grants{}},
	}}).Access
	if _, ok := kept.Grants.(uncomparable); !ok || kept.Authorizer != nil {
		t.Errorf("withAccess replaced an Access holding an uncomparable Grants: %+v", kept)
	}
}

package app

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccRuntimeAccessModule: the runtime builds the access module over its
// own store, authenticator and token service, with the configured access
// settings, and gives the same API to the router and to the Debian snapshot
// work.
func TestAccRuntimeAccessModule(t *testing.T) {
	var router server.Config
	var snapshot *itemkit.Kit
	origHandler, origSnapshot := newHandler, registerDebianSnapshotWork
	newHandler = func(c server.Config) http.Handler {
		router = c
		return http.NotFoundHandler()
	}
	registerDebianSnapshotWork = func(q *work.Queue, k *itemkit.Kit) error {
		snapshot = k
		return origSnapshot(q, k)
	}
	t.Cleanup(func() { newHandler, registerDebianSnapshotWork = origHandler, origSnapshot })

	cfg := testConfig(t)
	cfg.Listen = "127.0.0.1:0"
	cfg.Token.AllowBasicAuth = true
	cfg.Environments.GlobalLimit = 7
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ready) }()
	// The store stays open until run returns, which must happen before the
	// temporary directory is removed.
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	<-ready

	api := router.Access
	if !reflect.DeepEqual(snapshot.Access, api) {
		t.Error("the Debian snapshot work was given another access API than the router")
	}
	want := accessapi.Settings{AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 7}
	if api.Settings != want {
		t.Errorf("Access.Settings = %+v, want %+v from the configuration", api.Settings, want)
	}
	// The API answers over the router's own token service, authenticator
	// and store.
	issued, err := api.Tokens.Issue(ctx, accessapi.TokenRequest{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.Tokens.Verify(ctx, issued.AccessToken); err != nil {
		t.Errorf("the router's token service does not verify a token the API issued: %v", err)
	}
	p, err := api.Authenticator.Token(ctx, issued.AccessToken, "")
	if err != nil || p.Name != "admin" {
		t.Errorf("Access.Authenticator.Token = %+v, %v; want admin", p, err)
	}
	if err := api.AnonymousAccess.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := router.Auth.AnonymousAllowed(ctx); err != nil || !on {
		t.Errorf("the router's authenticator reads anonymous access %v, %v; want on", on, err)
	}
	var on bool
	err = router.Store.Settings().Get(ctx, types.SettingAnonymousAccess, &on)
	if err != nil || !on {
		t.Errorf("the router's store reads anonymous access %v, %v; want on", on, err)
	}
}

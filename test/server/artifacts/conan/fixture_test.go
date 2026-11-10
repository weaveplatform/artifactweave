package conan_test

// This file holds the Conan suite's fixtures: adapters over
// test/server/support/harness and test/server/support under the names the
// tests had in internal/server, and a token service built as the root's
// seedTokens builds it. The router is the harness's, so a change to its
// assembly reaches these tests without an edit here.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/token"
	"github.com/weaveplatform/artifactweave/migration/app"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

// itemWorld is the harness's item world under the fields the tests read.
type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
}

func newItemWorld(t *testing.T, anonymous bool, tune ...harness.Tune) *itemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, anonymous, tune...)
	return &itemWorld{t: w.T, h: w.H, db: w.DB, blobs: w.Blobs}
}

func (w *itemWorld) world() *harness.ItemWorld {
	return &harness.ItemWorld{T: w.t, H: w.h, DB: w.db, Blobs: w.blobs}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Do(method, path, as, body, headers...)
}

var testHash = support.TestHash

// sourceIdentity resolves every remote repository to one fixed HTTP source
// identity, as the root fixture of the same name does.
func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

// sourceBackend is a remote source transport made of one function.
type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func md5Hex(s string) string {
	b := md5.Sum([]byte(s)) //nolint:gosec // wire checksum
	return hex.EncodeToString(b[:])
}

func setTrashcan(t *testing.T, db store.Store, on bool) {
	t.Helper()
	if err := db.Settings().Put(context.Background(), types.SettingTrashcan,
		types.TrashcanSettings{Enabled: on, RetentionDays: 14}); err != nil {
		t.Fatal(err)
	}
}

// conanTestArchive is a Conan archive holding one file.
func conanTestArchive(t *testing.T, name, content string) string {
	return conanTestArchiveEntries(t, map[string]string{name: content})
}

func conanTestArchiveEntries(t *testing.T, entries map[string]string) string {
	t.Helper()
	return support.TarGz(t, entries)
}

// seedTokens gives db an instance identity and returns a token service over
// it. The root fixture of the same name also seeds the token records that
// the status tables address and returns their secrets; no test here reads
// them, so the second result is always nil.
func seedTokens(t *testing.T, db store.Store) (*token.Service, map[string]string) {
	t.Helper()
	ctx := context.Background()
	if err := db.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tokens, err := token.New(ctx, db, token.Options{KeyBits: 1024})
	if err != nil {
		t.Fatal(err)
	}
	return tokens, nil
}

// remoteWorld is the harness's remote world under the fields the tests had
// in migration/app: a test changes c and saves it with update, or replaces h
// with a router built with other options.
type remoteWorld struct {
	t  *testing.T
	rt *app.Runtime
	h  http.Handler
	up *fakeupstream.Server
	c  repo.Config
}

func newRemoteWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t)
	return &remoteWorld{t: w.T, rt: w.Runtime, h: w.H, up: w.Upstream, c: w.Repo}
}

// world is w as the harness's remote world, with the handler and the
// repository the test may have replaced.
func (w *remoteWorld) world() *harness.RemoteWorld {
	return &harness.RemoteWorld{T: w.t, Runtime: w.rt, H: w.h, Upstream: w.up, Repo: w.c}
}

func (w *remoteWorld) request(
	method, path, user, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.world().Request(method, path, user, body, headers...)
}

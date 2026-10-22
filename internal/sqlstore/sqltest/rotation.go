package sqltest

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Opener opens the same database each call with the given keyring (nil for
// none).
type Opener func(t *testing.T, keys *crypt.Keyring) *sqlcommon.Store

// sealedValues is the number of sealed values Rotation writes, one per
// sealed column.
const sealedValues = 11

// Rotation is decision 0017's rotation test over a populated store: every
// sealed column gets a value under version 1, Reseal under versions 1 and 2
// moves all of them to version 2, a second run changes nothing, and a store
// holding only version 2 reads every value back.
func Rotation(t *testing.T, open Opener) {
	t.Helper()
	ctx := context.Background()
	v1 := keyring(t, crypt.MasterKey{Version: 1, Material: []byte(strings.Repeat("1", 32))})
	v2 := crypt.MasterKey{Version: 2, Material: []byte(strings.Repeat("2", 32))}
	both := keyring(t, crypt.MasterKey{Version: 1, Material: []byte(strings.Repeat("1", 32))}, v2)
	only2 := keyring(t, v2)

	populate(t, open(t, v1))

	s := open(t, nil)
	if _, err := s.Reseal(ctx); !errors.Is(err, sqlcommon.ErrNoKeyring) {
		t.Fatalf("Reseal without a keyring: %v", err)
	}
	if _, err := s.Repos().Get(ctx, "remote"); !errors.Is(err, sqlcommon.ErrNoKeyring) {
		t.Fatalf("reading a sealed value without a keyring: %v", err)
	}
	if _, err := open(t, only2).Repos().Get(ctx, "remote"); !errors.Is(err, crypt.ErrUnknownKey) {
		t.Fatalf("reading a version-1 value with only version 2: %v", err)
	}

	s = open(t, both)
	n, err := s.Reseal(ctx)
	if err != nil || n != sealedValues {
		t.Fatalf("Reseal = %d, %v; want %d", n, err, sealedValues)
	}
	if n, err := s.Reseal(ctx); err != nil || n != 0 {
		t.Fatalf("second Reseal = %d, %v; want 0", n, err)
	}
	verify(t, open(t, only2))
}

func keyring(t *testing.T, keys ...crypt.MasterKey) *crypt.Keyring {
	t.Helper()
	k, err := crypt.NewKeyring(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// instanceKey is a fixed Ed25519 key so the test is deterministic.
var instanceKey = ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", ed25519.SeedSize)))

// populate writes one value into every sealed column.
func populate(t *testing.T, s *sqlcommon.Store) {
	t.Helper()
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.Repos().Create(ctx, repo.Config{
		Key:         "remote",
		Class:       repo.Remote,
		PackageType: repo.Generic,
		Password:    "repo-pw",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Token", Value: "header-secret", Sensitive: true},
		},
	}))
	check(s.Keys().CreateKeypair(ctx, types.Keypair{
		Name: "kp", Type: types.KeypairGPG,
		PrivateKey: "kp-private", Passphrase: "kp-pass",
	}))
	check(s.Keys().PutSigningKey(ctx, types.SigningKey{
		Alias:      "sk",
		PrivateKey: "sk-private", Passphrase: "sk-pass", IsDefault: true,
	}))
	check(s.Replications().Put(ctx, types.Replication{
		RepoKey: "remote", Key: "rep",
		URL: "https://peer.example/artifactory/remote", Password: "rep-pw",
	}))
	check(s.Webhooks().Create(ctx, types.WebhookSubscription{
		Key:        "hook",
		Enabled:    true,
		Domain:     types.DomainArtifact,
		EventTypes: []string{"deployed"},
		Handlers: []types.WebhookHandler{
			{Type: types.HandlerWebhook, URL: "https://hook.example", Secret: "hook-secret"},
		},
	}))
	check(s.Configs().Put(ctx, types.ConfigDocument{
		Kind: types.ConfigLDAP,
		Key:  "ldap",
		Document: []byte(
			`{"ldap_url":"ldap://dir"}`,
		),
		Secrets: map[string]string{"manager_password": "ldap-pw"},
	}))
	check(s.Workers().Create(ctx, types.Worker{
		Key: "w", Action: types.WorkerAction("BEFORE_DOWNLOAD"),
		SourceCode: "export default async () => ({})",
	}))
	check(s.Workers().PutSecret(ctx, "w", "token", "worker-secret"))
	check(s.Instance().Init(ctx, types.InstanceIdentity{
		InstanceID: "jfrt@01rotation",
		PublicKey:  instanceKey.Public().(ed25519.PublicKey), PrivateKey: instanceKey,
	}))
}

// verify reads every sealed value back.
func verify(t *testing.T, s *sqlcommon.Store) {
	t.Helper()
	ctx := context.Background()
	want := func(what, got, value string, err error) {
		t.Helper()
		if err != nil || got != value {
			t.Fatalf("%s = %q, %v; want %q", what, got, err, value)
		}
	}
	r, err := s.Repos().Get(ctx, "remote")
	want("repository password", r.Password, "repo-pw", err)
	if len(r.CustomHTTPHeaders) != 1 {
		t.Fatal("missing repository header after rotation")
	}
	want("repository header", r.CustomHTTPHeaders[0].Value, "header-secret", err)
	kp, err := s.Keys().Keypair(ctx, "kp")
	want("key pair private key", kp.PrivateKey, "kp-private", err)
	want("key pair passphrase", kp.Passphrase, "kp-pass", err)
	sk, err := s.Keys().SigningKey(ctx, "sk")
	want("signing key", sk.PrivateKey+"/"+sk.Passphrase, "sk-private/sk-pass", err)
	rep, err := s.Replications().Get(ctx, "rep")
	want("replication password", rep.Password, "rep-pw", err)
	h, err := s.Webhooks().Get(ctx, "hook")
	if err != nil || len(h.Handlers) != 1 {
		t.Fatalf("webhook = %+v, %v", h, err)
	}
	want("webhook secret", h.Handlers[0].Secret, "hook-secret", nil)
	cd, err := s.Configs().Get(ctx, types.ConfigLDAP, "ldap")
	want("config secret", cd.Secrets["manager_password"], "ldap-pw", err)
	ws, err := s.Workers().Secrets(ctx, "w")
	want("worker secret", ws["token"], "worker-secret", err)
	id, err := s.Instance().Get(ctx)
	if err != nil || !id.PrivateKey.Equal(instanceKey) {
		t.Fatalf("instance key = %v, %v", id.PrivateKey, err)
	}
}

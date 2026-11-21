package federation_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func pushAccess() accessapi.API {
	return accessapi.API{Projects: &accessapitest.Projects{}, Authenticator: &accessapitest.Authenticator{}, Authorizer: &accessapitest.Authorizer{}}
}

func TestUnitPushLifecycleKeepsConfigurationConstructionIndependent(t *testing.T) {
	store := inmem.New()
	m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Pusher(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("unopened driver available", err)
	}
	if err := m.OpenPush(t.Context(), nil, federation.PushOptions{}); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing source accepted", err)
	}
	source := struct{ artifactsapi.TransferSource }{}
	if err := m.OpenPush(t.Context(), source, federation.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Pusher(); err != nil {
		t.Fatal(err)
	}
	if err := m.OpenPush(t.Context(), source, federation.PushOptions{}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("double initialization admitted", err)
	}
	m.Close()
	m.Close()
	if _, err := m.Pusher(); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("closed driver available", err)
	}
	if err := m.OpenPush(t.Context(), source, federation.PushOptions{}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("closed module reopened", err)
	}
	(*federation.Module)(nil).Close()
}

func TestUnitPushRestoresOnlyCurrentOriginalCredentialOutsideTransaction(t *testing.T) {
	boom := errors.New("current authority unavailable")
	for _, test := range []struct {
		name string
		want error
	}{
		{"current", nil},
		{"registry", nil},
		{"guard", boom},
		{"missing", kernel.ErrBadCredentials},
		{"credential", boom},
		{"anonymous", kernel.ErrBadCredentials},
		{"permissions", boom},
		{"nil decider", kernel.ErrForbidden},
		{"typed nil decider", kernel.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := inmem.New()
			reference := kernel.CredentialReference{Name: "original", Password: true, PasswordRevision: [32]byte{8}}
			if test.name == "registry" {
				reference = kernel.CredentialReference{Name: "original", TokenID: "original-token", Registry: &kernel.RegistryReference{
					Verified: true,
					Scope:    "artifact:source:r",
					Access:   kernel.RegistryAccess{Realm: "source", Grants: []kernel.RegistryGrant{{Type: "repository", Name: "selected", Actions: []string{"pull"}}}},
				}}
			}
			steps := []string{}
			checkTx := func(ctx context.Context) {
				t.Helper()
				if !store.InTx(ctx) {
					t.Fatal("authority refresh outside guarded transaction")
				}
			}
			access := pushAccess()
			access.Projects = &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
				checkTx(ctx)
				steps = append(steps, "guard")
				if test.name == "guard" {
					return boom
				}
				return nil
			}}
			refresh := func(ctx context.Context, principal kernel.Principal) (kernel.Principal, error) {
				checkTx(ctx)
				steps = append(steps, "credential")
				if principal.Admin || len(principal.Groups) != 0 || principal.Name != "original" {
					t.Fatal("reference restored cached authority or lost original credential")
				}
				if !reflect.DeepEqual(principal.CredentialReference(), reference) {
					t.Fatal("restoration lost retained credential binding")
				}
				if test.name == "missing" {
					return kernel.Principal{}, kernel.ErrNotFound
				}
				if test.name == "credential" {
					return kernel.Principal{}, boom
				}
				if test.name == "anonymous" {
					return kernel.Anonymous, nil
				}
				principal.Groups = []string{"current-group"}
				return principal, nil
			}
			access.Authenticator = &accessapitest.Authenticator{
				CurrentFunc: func(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
					if test.name == "registry" {
						t.Fatal("registry credential treated as HTTP")
					}
					return refresh(ctx, p)
				},
				CurrentRegistryFunc: func(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
					if p.Registry == nil || p.Password || p.TokenID != "original-token" || p.Scope.String() != "artifact:source:r" {
						t.Fatal("retained registry narrowing lost")
					}
					return refresh(ctx, p)
				},
			}
			access.Authorizer = &accessapitest.Authorizer{DecideFunc: func(ctx context.Context, subject accessapi.Subject, _ bool) (accessapi.Decider, error) {
				checkTx(ctx)
				steps = append(steps, "permissions")
				if subject.Admin || !reflect.DeepEqual(subject.Groups, []string{"current-group"}) {
					t.Fatal("policy received stale authority")
				}
				if test.name == "permissions" {
					return nil, boom
				}
				if test.name == "nil decider" {
					return nil, nil
				}
				if test.name == "typed nil decider" {
					return (*accessapitest.Decider)(nil), nil
				}
				return &accessapitest.Decider{}, nil
			}}
			m, err := federation.New(federation.Deps{Store: store, Access: access})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(m.Close)
			if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
				t.Fatal(err)
			}
			push, err := m.Pusher()
			if err != nil {
				t.Fatal(err)
			}
			original := reference.Principal()
			original.Admin, original.Groups = true, []string{"stale-group"}
			ctx, err := push.Restore(t.Context(), original.CredentialReference())
			if !errors.Is(err, test.want) {
				t.Fatal("restore classification", err, test.want)
			}
			if err != nil {
				if ctx != nil {
					t.Fatal("failed restore returned caller")
				}
				return
			}
			if store.InTx(ctx) {
				t.Fatal("restoration leaked transaction")
			}
			current, ok := kernel.FromContext(ctx)
			if !ok || current.Name != "original" || current.Admin || !reflect.DeepEqual(current.Groups, []string{"current-group"}) {
				t.Fatal("restored caller did not preserve verified identity")
			}
			if _, ok := accessapi.DeciderFrom(ctx); !ok {
				t.Fatal("current permission decision not bound")
			}
			if !reflect.DeepEqual(steps, []string{"guard", "credential", "permissions"}) {
				t.Fatal("authority refresh order", steps)
			}
			if err := store.Tx(t.Context(), func(ctx context.Context) error {
				_, err := push.Restore(ctx, original.CredentialReference())
				return err
			}); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("transactional restoration admitted", err)
			}
		})
	}
}

func TestUnitPushConnectionPolicyIdentitySurvivesRestartWithSeparatePurposeKey(t *testing.T) {
	store := inmem.New()
	var revisions []string
	for range 2 {
		m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err != nil {
			t.Fatal(err)
		}
		push, err := m.Pusher()
		if err != nil {
			t.Fatal(err)
		}
		err = store.Tx(t.Context(), func(ctx context.Context) error {
			connection, err := push.Connections.Connection(
				ctx,
				model.Replication{RepoKey: "source", Key: "target", URL: "https://target.invalid/artifactory/repo", Password: "secret", SocketTimeout: time.Second},
			)
			if err == nil {
				revisions = append(revisions, connection.Revision())
			}
			return err
		})
		m.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(revisions) != 2 || revisions[0] == "" || revisions[0] != revisions[1] {
		t.Fatal("restart changed transport binding")
	}
	document, err := store.Configs().Get(t.Context(), configdoc.ConfigKind("replication_internal"), "connection_policy")
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(document.Secrets["revision_key"])
	if err != nil || len(key) != 32 {
		t.Fatal("missing durable private key")
	}
	encoded, err := json.Marshal(document)
	if err != nil || bytes.Contains(encoded, []byte(document.Secrets["revision_key"])) {
		t.Fatal("document serialization exposed private secret")
	}
	for _, name := range []string{"replication.internal_policy_key", "remote.internal_policy_key"} {
		var value []byte
		if err := store.Settings().Get(t.Context(), name, &value); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("private key leaked into general settings or borrowed remote key", err)
		}
	}
}

func TestUnitPushRefusesCorruptPersistedPolicyKeyWithoutReplacement(t *testing.T) {
	for _, test := range []struct{ name, value string }{{"missing", ""}, {"malformed", "invalid!"}, {"short", base64.StdEncoding.EncodeToString([]byte("short"))}, {"zero", base64.StdEncoding.EncodeToString(make([]byte, 32))}} {
		t.Run(test.name, func(t *testing.T) {
			store := inmem.New()
			kind := configdoc.ConfigKind("replication_internal")
			if err := store.Configs().
				Put(t.Context(), configdoc.ConfigDocument{Kind: kind, Key: "connection_policy", Document: []byte(`{}`), Secrets: map[string]string{"revision_key": test.value}}); err != nil {
				t.Fatal(err)
			}
			m, err := federation.New(federation.Deps{Store: store, Access: pushAccess()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(m.Close)
			if err := m.OpenPush(t.Context(), struct{ artifactsapi.TransferSource }{}, federation.PushOptions{}); err == nil {
				t.Fatal("corrupt persisted key accepted")
			}
			document, err := store.Configs().Get(t.Context(), kind, "connection_policy")
			if err != nil || document.Secrets["revision_key"] != test.value {
				t.Fatal("corrupt key was silently replaced", err)
			}
			leases, err := store.Leases().List(t.Context())
			if err != nil || len(leases) != 0 {
				t.Fatal("failed initialization retained startup lease", err)
			}
		})
	}
}

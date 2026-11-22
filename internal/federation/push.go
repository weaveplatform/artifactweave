package federation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/adapters/pushhttp"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// PushOptions is immutable trusted HTTP policy supplied by the application.
// Empty trust roots use system trust. Private origins are exact operator grants.
type PushOptions struct {
	RootCAsPEM            []byte
	AllowedPrivateOrigins []string
	LookupIP              func(context.Context, string) ([]net.IPAddr, error)
}

type pushStore interface {
	Store
	port.PushStore
	Leases() work.LeaseStore
}

// OpenPush initializes single push after Artifacts is assembled. It reads or
// initializes a stable purpose-specific private key and opens an idle HTTP pool;
// no DNS, target request, worker or run admission occurs. App calls it once at
// startup and calls Close after stopping the workers that use Pusher.
func (m *Module) OpenPush(ctx context.Context, source artifactsapi.TransferSource, options PushOptions) error {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if m.closed || m.push != nil {
		return fmt.Errorf("replication push lifecycle already ended or opened: %w", kernel.ErrConflict)
	}
	store, ok := m.deps.Store.(pushStore)
	if !ok || source == nil || m.deps.Access.Projects == nil || m.deps.Access.Authenticator == nil || m.deps.Access.Authorizer == nil {
		return fmt.Errorf("single-push application contracts unavailable: %w", kernel.ErrStore)
	}
	if store.InTx(ctx) {
		return fmt.Errorf("open single push inside metadata transaction: %w", kernel.ErrInvalid)
	}
	key, err := m.pushPolicyKey(ctx, store)
	if err != nil {
		return err
	}
	connections, err := pushhttp.New(store, pushhttp.Options{
		RevisionKey: key, RootCAsPEM: options.RootCAsPEM,
		AllowedPrivateOrigins: options.AllowedPrivateOrigins, LookupIP: options.LookupIP,
	})
	if err != nil {
		return fmt.Errorf("open replication target connections: %w", err)
	}
	m.pushConnections = connections
	m.push = &replication.Push{Store: store, Configurations: m.Configurations(), Source: source, Connections: connections, Restore: m.restoreTransferCaller}
	return nil
}

// Pusher returns the initialized bounded transfer driver. The dispatch owner
// still owns run admission, exclusive work ownership and final lease fencing.
func (m *Module) Pusher() (replication.Push, error) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	if m.closed || m.push == nil {
		return replication.Push{}, fmt.Errorf("single push is not open: %w", kernel.ErrStore)
	}
	return *m.push, nil
}

// Close stops new target exchanges and releases idle sockets. Active exchanges
// belong to the worker contexts, which the application cancels and joins first.
func (m *Module) Close() {
	if m == nil {
		return
	}
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.closed = true
	if m.pushConnections != nil {
		m.pushConnections.Close()
	}
}

func (m *Module) restoreTransferCaller(ctx context.Context, reference kernel.CredentialReference) (context.Context, error) {
	store, ok := m.deps.Store.(interface{ InTx(context.Context) bool })
	if !ok || store.InTx(ctx) {
		return nil, fmt.Errorf("restore replication caller needs an external context: %w", kernel.ErrInvalid)
	}
	var principal kernel.Principal
	var decider accessapi.Decider
	err := m.deps.Store.Tx(ctx, func(tx context.Context) error {
		if err := m.deps.Access.Projects.Guard(tx); err != nil {
			return fmt.Errorf("guard replication caller: %w", err)
		}
		var err error
		principal = reference.Principal()
		if principal.Registry != nil {
			principal, err = m.deps.Access.Authenticator.CurrentRegistry(tx, principal)
		} else {
			principal, err = m.deps.Access.Authenticator.Current(tx, principal)
		}
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrBadCredentials
		}
		if err != nil {
			return fmt.Errorf("current replication caller credential: %w", err)
		}
		if principal.Anonymous || principal.Name == "" {
			return kernel.ErrBadCredentials
		}
		decider, err = m.deps.Access.Authorizer.For(tx, principal)
		if err != nil {
			return fmt.Errorf("current replication caller permissions: %w", err)
		}
		if decider == nil {
			return kernel.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("restore replication caller: %w", err)
	}
	// Never return the transaction context: the next action may be an HTTP
	// transfer. Artifacts refreshes current content authority at its own fences.
	bound := accessapi.WithCaller(ctx, principal, decider)
	if _, ok := accessapi.DeciderFrom(bound); !ok {
		return nil, kernel.ErrForbidden
	}
	return bound, nil
}

// These private singleton identities are persisted protocol state. The random
// key lives in ConfigDocument.Secrets so ordinary config-secret sealing/reseal
// preserves its value across master-key rotation; active-key derivation cannot.
const (
	pushPolicyKeySetting                      = "replication.internal_policy_key"
	pushPolicyKeyKind    configdoc.ConfigKind = "replication_internal"
	pushPolicyKeyName                         = "connection_policy"
	pushPolicyKeySecret                       = "revision_key"
)

func (m *Module) pushPolicyKey(ctx context.Context, store pushStore) ([]byte, error) {
	var key []byte
	err := store.Tx(ctx, func(tx context.Context) error {
		holder := rand.Text()
		now := time.Now().UTC()
		held, err := store.Leases().Acquire(tx, work.Lease{Name: pushPolicyKeySetting, Holder: holder, Acquired: now, Expires: now.Add(time.Minute)}, now)
		if err != nil {
			return fmt.Errorf("lock replication policy key: %w", err)
		}
		if !held {
			return fmt.Errorf("replication policy key initializer is busy: %w", kernel.ErrConflict)
		}
		document, err := store.Configs().Get(tx, pushPolicyKeyKind, pushPolicyKeyName)
		if errors.Is(err, kernel.ErrNotFound) {
			key = make([]byte, 32)
			if _, err = rand.Read(key); err != nil {
				return fmt.Errorf("generate replication policy key: %w", err)
			}
			document = configdoc.ConfigDocument{
				Kind:     pushPolicyKeyKind,
				Key:      pushPolicyKeyName,
				Document: []byte(`{}`),
				Secrets:  map[string]string{pushPolicyKeySecret: base64.StdEncoding.EncodeToString(key)},
				Updated:  now,
			}
			err = store.Configs().Put(tx, document)
		}
		if err != nil {
			return fmt.Errorf("persist replication policy key: %w", err)
		}
		key, err = base64.StdEncoding.DecodeString(document.Secrets[pushPolicyKeySecret])
		if err != nil || len(key) != 32 {
			return fmt.Errorf("invalid replication policy key: %w", kernel.ErrStore)
		}
		if err := store.Leases().Release(tx, pushPolicyKeySetting, holder); err != nil {
			return fmt.Errorf("unlock replication policy key: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("replication policy key: %w", err)
	}
	return key, nil
}

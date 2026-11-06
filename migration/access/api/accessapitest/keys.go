package accessapitest

import (
	"context"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

var _ accessapi.Keys = (*Keys)(nil)

// Keys is a Keys over maps, which it only reads: key pairs by name and
// certificates by alias. A name or alias missing from its map answers an
// error matching kernel.ErrNotFound; Err, when set, answers every call
// instead, as a store that cannot be read does. It may be used from several
// goroutines while no one changes its fields.
type Keys struct {
	// Keypairs maps a key pair's name to it.
	Keypairs map[string]accessapi.Keypair
	// Certificates maps a certificate's alias to it.
	Certificates map[string]accessapi.Certificate
	// Err, when set, is every call's error.
	Err error
}

// Keypair returns the key pair called name, Err, or an error matching
// kernel.ErrNotFound when Keypairs has no such name.
func (k *Keys) Keypair(_ context.Context, name string) (accessapi.Keypair, error) {
	if k.Err != nil {
		return accessapi.Keypair{}, k.Err
	}
	kp, ok := k.Keypairs[name]
	if !ok {
		return accessapi.Keypair{}, fmt.Errorf(
			"accessapitest: key pair %q: %w",
			name,
			kernel.ErrNotFound,
		)
	}
	return kp, nil
}

// Certificate returns the certificate with alias, Err, or an error matching
// kernel.ErrNotFound when Certificates has no such alias.
func (k *Keys) Certificate(_ context.Context, alias string) (accessapi.Certificate, error) {
	if k.Err != nil {
		return accessapi.Certificate{}, k.Err
	}
	c, ok := k.Certificates[alias]
	if !ok {
		return accessapi.Certificate{}, fmt.Errorf(
			"accessapitest: certificate %q: %w", alias, kernel.ErrNotFound,
		)
	}
	return c, nil
}

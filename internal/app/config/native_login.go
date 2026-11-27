package config

import (
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

// NativeLogin enables the shared npm/Terraform browser approval page. Its
// canonical origin must be explicit; ordinary proxy/override headers cannot
// choose where a client receives a credential.
type NativeLogin struct {
	Enabled bool   `yaml:"enabled"`
	Origin  string `yaml:"origin"`
}

func (n NativeLogin) validate() []error {
	if n.Enabled && !accessapi.ValidNativeOrigin(n.Origin) {
		return []error{fmt.Errorf("%w: native_login.origin requires a canonical HTTPS origin (HTTP only on loopback)", ErrInvalidValue)}
	}
	return nil
}

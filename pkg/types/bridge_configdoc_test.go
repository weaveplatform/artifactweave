package types_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The bridge must alias the document and its kind, not declare its own
// (decision 0044, "Type-alias bridges"): the stores build types.ConfigDocument
// values that the configdoc.ConfigStore methods take and return, and every
// kind is a ConfigKind. A struct is assignable only to its own type, and a
// value of a named string type only to that type, so these lines compile only
// while each pair is one type.
var (
	_ configdoc.ConfigDocument = types.ConfigDocument{}
	_ configdoc.ConfigKind     = types.ConfigKind("")
)

// A constant has no identity beyond its type and value, so the bridged kinds
// are compared on both. The kind strings are stored in the config documents
// table: a copy whose value drifted would make callers of the two names read
// and write different documents, and an untyped copy, though it still
// compiles at every use, is a plain string, not a ConfigKind, wherever it is
// boxed.
func TestUnitBridgeConfigDocKinds(t *testing.T) {
	for _, c := range []struct{ legacy, target any }{
		{types.ConfigOIDC, configdoc.ConfigOIDC},
		{types.ConfigOIDCMapping, configdoc.ConfigOIDCMapping},
		{types.ConfigLDAP, configdoc.ConfigLDAP},
		{types.ConfigLDAPGroup, configdoc.ConfigLDAPGroup},
		{types.ConfigSAML, configdoc.ConfigSAML},
		{types.ConfigCrowd, configdoc.ConfigCrowd},
		{types.ConfigHTTPSSO, configdoc.ConfigHTTPSSO},
		{types.ConfigVault, configdoc.ConfigVault},
		{types.ConfigProxy, configdoc.ConfigProxy},
		{types.ConfigPropertySet, configdoc.ConfigPropertySet},
		{types.ConfigLayout, configdoc.ConfigLayout},
		{types.ConfigBackup, configdoc.ConfigBackup},
		{types.ConfigMailServer, configdoc.ConfigMailServer},
		{types.ConfigReverseProxy, configdoc.ConfigReverseProxy},
		{types.ConfigGPGSigningKey, configdoc.ConfigGPGSigningKey},
		{types.ConfigCurationSettings, configdoc.ConfigCurationSettings},
	} {
		if c.legacy != c.target {
			t.Errorf("types holds %#[1]v (%[1]T), configdoc holds %#[2]v (%[2]T)",
				c.legacy, c.target)
		}
	}
}

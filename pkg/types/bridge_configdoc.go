// Bridge (P9d): declared in migration/platform/configdoc; PRO-02 deletes it.

package types

import "github.com/weaveplatform/artifactweave/migration/platform/configdoc"

type (
	ConfigKind     = configdoc.ConfigKind
	ConfigDocument = configdoc.ConfigDocument
)

const (
	ConfigOIDC             = configdoc.ConfigOIDC
	ConfigOIDCMapping      = configdoc.ConfigOIDCMapping
	ConfigLDAP             = configdoc.ConfigLDAP
	ConfigLDAPGroup        = configdoc.ConfigLDAPGroup
	ConfigSAML             = configdoc.ConfigSAML
	ConfigCrowd            = configdoc.ConfigCrowd
	ConfigHTTPSSO          = configdoc.ConfigHTTPSSO
	ConfigVault            = configdoc.ConfigVault
	ConfigProxy            = configdoc.ConfigProxy
	ConfigPropertySet      = configdoc.ConfigPropertySet
	ConfigLayout           = configdoc.ConfigLayout
	ConfigBackup           = configdoc.ConfigBackup
	ConfigMailServer       = configdoc.ConfigMailServer
	ConfigReverseProxy     = configdoc.ConfigReverseProxy
	ConfigGPGSigningKey    = configdoc.ConfigGPGSigningKey
	ConfigCurationSettings = configdoc.ConfigCurationSettings
)

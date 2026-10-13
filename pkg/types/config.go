package types

import "time"

// ConfigKind names a family of configuration documents that are stored and
// returned whole and never queried by field: identity-provider settings, the
// YAML descriptor sections that are not repositories or security, and the
// external secret backends. They share one table (research 15 section 4)
// because a column per wire field would only mirror the document.
type ConfigKind string

// Configuration document kinds.
const (
	ConfigOIDC             ConfigKind = "oidc"
	ConfigOIDCMapping      ConfigKind = "oidc_identity_mapping"
	ConfigLDAP             ConfigKind = "ldap"
	ConfigLDAPGroup        ConfigKind = "ldap_group"
	ConfigSAML             ConfigKind = "saml"
	ConfigCrowd            ConfigKind = "crowd"
	ConfigHTTPSSO          ConfigKind = "http_sso"
	ConfigVault            ConfigKind = "vault"
	ConfigProxy            ConfigKind = "proxy"
	ConfigPropertySet      ConfigKind = "property_set"
	ConfigLayout           ConfigKind = "layout"
	ConfigBackup           ConfigKind = "backup"
	ConfigMailServer       ConfigKind = "mail_server"
	ConfigReverseProxy     ConfigKind = "reverse_proxy"
	ConfigGPGSigningKey    ConfigKind = "gpg_signing_key"
	ConfigCurationSettings ConfigKind = "curation"
)

// ConfigDocument is one configuration document. Document is the JSON the
// owning service defines (usually its wire form with secrets removed);
// Secrets holds the removed values by name and is sealed at rest, so a read
// of Document can never leak one. Key is unique within Kind; OIDC identity
// mappings use "<provider>/<name>".
type ConfigDocument struct {
	Kind     ConfigKind
	Key      string
	Enabled  bool
	Document []byte
	Secrets  map[string]string `json:"-"`
	Updated  time.Time
}

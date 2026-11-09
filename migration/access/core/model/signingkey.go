package model

// SigningKey signs bundle manifests. The private key and passphrase are
// sealed and never serialised.
type SigningKey struct {
	Alias      string
	PublicKey  string
	PrivateKey string `json:"-"`
	Passphrase string `json:"-"`
	IsDefault  bool
}

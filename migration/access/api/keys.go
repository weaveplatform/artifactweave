package api

import "time"

// KeypairType is the kind of a stored key pair.
type KeypairType string

// Key pair types the security API accepts.
const (
	KeypairRSA     KeypairType = "RSA"
	KeypairGPG     KeypairType = "GPG"
	KeypairEd25519 KeypairType = "Ed25519"
)

// Keypair is a named signing key pair (the security API's pairName). The
// private key and passphrase are sealed at rest (decision 0017).
type Keypair struct {
	Name       string
	Type       KeypairType
	Alias      string
	PublicKey  string
	PrivateKey string `json:"-"`
	Passphrase string `json:"-"`
}

// Certificate is a client certificate that remote repositories present to
// upstreams (clientTlsCertificate). PEM holds the certificate chain and
// PrivateKey its key, sealed at rest (decision 0017). Every other field is
// parsed from the PEM once at upload, so listings never re-parse.
type Certificate struct {
	Alias       string
	PEM         string
	PrivateKey  string `json:"-"`
	IssuedTo    string
	IssuedBy    string
	IssuedOn    time.Time
	ValidUntil  time.Time
	Fingerprint string
}

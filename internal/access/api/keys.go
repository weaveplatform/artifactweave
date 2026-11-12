package api

import (
	"context"
	"time"
)

// KeypairType is the kind of a stored key pair.
type KeypairType string

// Key pair types the security API accepts.
const (
	KeypairRSA     KeypairType = "RSA"
	KeypairGPG     KeypairType = "GPG"
	KeypairEd25519 KeypairType = "Ed25519"
	// KeypairECDSA signs npm packages (decision 0034, "Signatures").
	KeypairECDSA KeypairType = "ECDSA"
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

// Keys reads the key material other modules sign or connect with: the
// signing key pairs of repositories and the client certificates of remote
// repositories. Private keys and passphrases come back unsealed, ready to
// use, so a caller never logs or serialises them (decision 0017). Each call
// uses ctx for the store it reads, so a call inside a store transaction takes
// part in it.
type Keys interface {
	// Keypair returns the key pair called name; an error matching
	// kernel.ErrNotFound when there is none.
	Keypair(ctx context.Context, name string) (Keypair, error)
	// Certificate returns the certificate with alias; an error matching
	// kernel.ErrNotFound when there is none.
	Certificate(ctx context.Context, alias string) (Certificate, error)
}

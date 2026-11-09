package types

// KeypairRole is how a repository uses a key pair: the primary key signs
// metadata, the secondary is published for clients rotating to it.
type KeypairRole string

// Key pair roles.
const (
	KeypairPrimary   KeypairRole = "primary"
	KeypairSecondary KeypairRole = "secondary"
)

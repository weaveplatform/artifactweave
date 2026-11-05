package types

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
)

// ErrInvalidInstanceID reports an instance id that is not jfrt@<uuid>.
var ErrInvalidInstanceID = errors.New("invalid instance id")

// ProtocolVersion is the peer protocol version this build speaks
// (decision 0021); peers advertise theirs at pairing.
const ProtocolVersion = 1

var instanceIDPattern = regexp.MustCompile(
	`^jfrt@[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// ValidateInstanceID checks the jfrt@<uuid> form /api/system/service_id
// serves and peers identify themselves by.
func ValidateInstanceID(id string) error {
	if !instanceIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidInstanceID, id)
	}
	return nil
}

// InstanceIdentity is this instance's singleton identity, created at first
// start. The private key is sealed (decision 0017) and never serialised.
type InstanceIdentity struct {
	InstanceID      string
	PublicKey       ed25519.PublicKey
	PrivateKey      ed25519.PrivateKey `json:"-"`
	NamespaceID     string             // cold-storage namespace (decision 0024)
	ProtocolVersion int
}

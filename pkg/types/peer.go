package types

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidStream reports a stream name with an unknown form.
var ErrInvalidStream = errors.New("invalid stream name")

// UseCase is a reason two instances are paired.
type UseCase string

// Use cases: JFrog's name where it means the same thing, artifactweave's own
// otherwise (decision 0021).
const (
	UseFederation         UseCase = "federation"
	UseDistribution       UseCase = "distribution"
	UseAccessFederation   UseCase = "access-federation"
	UsePlatformFederation UseCase = "platform-federation"
)

// KnownUseCase reports whether u can be paired; JFrog use cases with no
// counterpart (mission-control, bridge, artifactory-cold) answer 404.
func KnownUseCase(u UseCase) bool {
	switch u {
	case UseFederation, UseDistribution, UseAccessFederation, UsePlatformFederation:
		return true
	default:
		return false
	}
}

// PeerStatus is a peer's health as the JPD APIs report it (decision 0025).
type PeerStatus string

// Peer statuses.
const (
	PeerOnline       PeerStatus = "ONLINE"
	PeerOffline      PeerStatus = "OFFLINE"
	PeerPartial      PeerStatus = "PARTIAL"
	PeerUnauthorized PeerStatus = "UNAUTHORIZED"
)

// Peer is another instance this one is paired with: a federation member, a
// distribution edge, an Access Federation target, a follower JPD or a
// topology site. Location fields are set by the administrator.
type Peer struct {
	InstanceID      string
	BaseURL         string
	PublicKey       ed25519.PublicKey
	ProtocolVersion int
	UseCases        []UseCase
	Status          PeerStatus
	LastSeen        time.Time
	DisplayName     string
	CityName        string
	CountryCode     string
	EdgeRole        bool
}

// Has reports whether the peer is paired for u.
func (p Peer) Has(u UseCase) bool { return slices.Contains(p.UseCases, u) }

// DeriveStatus computes the JPD status: UNAUTHORIZED when the last signed
// exchange was rejected, OFFLINE when nothing succeeded within the stale
// window, PARTIAL when some use cases are failing, ONLINE otherwise.
func (p Peer) DeriveStatus(
	now time.Time,
	stale time.Duration,
	rejected bool,
	failingUseCases int,
) PeerStatus {
	switch {
	case rejected:
		return PeerUnauthorized
	case p.LastSeen.IsZero() || now.Sub(p.LastSeen) > stale:
		return PeerOffline
	case failingUseCases > 0:
		return PeerPartial
	default:
		return PeerOnline
	}
}

// PairingNonce backs a single-use pairing token. Only the nonce hash is
// stored until the token is redeemed.
type PairingNonce struct {
	NonceHash  []byte
	UseCase    UseCase
	ExpiresAt  time.Time
	RedeemedAt time.Time
}

// PairingTokenTTL is how long a pairing token can be redeemed: 300 seconds,
// the JFrog pairing-token default (decision 0021).
const PairingTokenTTL = 300 * time.Second

// Usable reports whether the nonce can still complete a pairing.
func (n PairingNonce) Usable(now time.Time) bool {
	return n.RedeemedAt.IsZero() && now.Before(n.ExpiresAt)
}

// Stamp is an event's position for conflict resolution.
type Stamp struct {
	Time   HLC
	Origin string
}

// After reports whether s wins over o under last-writer-wins: later clock
// first, origin instance id as the tie-break so every member picks the same
// winner.
func (s Stamp) After(o Stamp) bool {
	if c := s.Time.Compare(o.Time); c != 0 {
		return c > 0
	}
	return s.Origin > o.Origin
}

// StreamKind is the family of an outbox stream.
type StreamKind string

// Stream kinds (decision 0021).
const (
	StreamRepository   StreamKind = "repo"
	StreamAccess       StreamKind = "access"
	StreamDistribution StreamKind = "distribution"
)

// RepositoryStream names a federated repository's stream.
func RepositoryStream(repoKey string) string { return string(StreamRepository) + ":" + repoKey }

// AccessStream is the Access Federation stream.
const AccessStream = string(StreamAccess)

// DistributionStream names one distribution job's stream.
func DistributionStream(trackerID int64, peerInstanceID string) string {
	return string(
		StreamDistribution,
	) + ":" + strconv.FormatInt(
		trackerID,
		10,
	) + ":" + peerInstanceID
}

// ParseStream splits a stream name into its kind and remainder.
func ParseStream(stream string) (StreamKind, string, error) {
	if stream == AccessStream {
		return StreamAccess, "", nil
	}
	kind, rest, ok := strings.Cut(stream, ":")
	if !ok || rest == "" {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidStream, stream)
	}
	switch StreamKind(kind) {
	case StreamRepository, StreamDistribution:
		return StreamKind(kind), rest, nil
	default:
		return "", "", fmt.Errorf("%w: %q", ErrInvalidStream, stream)
	}
}

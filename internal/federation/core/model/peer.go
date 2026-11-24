package model

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
)

// ErrInvalidStream reports a stream name with an unknown form.
var ErrInvalidStream = errors.New("invalid stream name")

// UseCase is a reason two instances are paired.
type UseCase string

// Use cases: JFrog's name where it means the same thing, artifactweave's own
// otherwise (decision 0021). The two federated-repository names are 7.x's
// ServiceTrustUseCase paths, which the pairing endpoints take verbatim, so
// they keep their slash and asterisk.
const (
	// UseFederatedRepo is two-way federated repository trust.
	UseFederatedRepo UseCase = "federated-repo/*"
	// UseFederatedRepoRead is receiving-only trust: the issuer sends events
	// to the redeemer and accepts none from it.
	UseFederatedRepoRead  UseCase = "federated-repo/r"
	UseDistribution       UseCase = "distribution"
	UseAccessFederation   UseCase = "access-federation"
	UsePlatformFederation UseCase = "platform-federation"
)

// KnownUseCase reports whether u can be paired; JFrog use cases with no
// counterpart (mission-control, bridge, artifactory-cold) answer 404.
func KnownUseCase(u UseCase) bool {
	switch u {
	case UseFederatedRepo, UseFederatedRepoRead, UseDistribution, UseAccessFederation,
		UsePlatformFederation:
		return true
	default:
		return false
	}
}

// Name is the service a use case names in 7.x's pairing messages ("Trust
// with service <name> was established successfully"): federated-repo for
// both federated-repository use cases, as ServiceTrustUseCase.getName gives,
// and the use case itself otherwise.
func (u UseCase) Name() string {
	if u == UseFederatedRepo || u == UseFederatedRepoRead {
		return "federated-repo"
	}
	return string(u)
}

// Pairing is how the service_trust pairing endpoints answer a use case.
type Pairing int

// Pairing answers (decision 0021, "Use cases").
const (
	// PairingUnknown is 404 "Unknown use case": anything 7.x's
	// ServiceTrustUseCase does not match or artifactweave has no
	// counterpart for, the artifactory-cold use cases among them (P37).
	PairingUnknown Pairing = iota
	// PairingServed is issued and redeemed: the federated-repository use
	// cases P13 serves.
	PairingServed
	// PairingPlanned keeps the 501 these use cases answered before P13:
	// platform-federation and access-federation (P14), distribution (P15),
	// and mission-control and bridge (P40).
	PairingPlanned
)

// PairingFor classifies the use case path of a service_trust pairing
// request. Both the Artifactory and the Access endpoint use it, so the two
// never disagree about a use case.
func PairingFor(path string) Pairing {
	switch UseCase(path) {
	case UseFederatedRepo, UseFederatedRepoRead:
		return PairingServed
	case UsePlatformFederation, UseAccessFederation, UseDistribution,
		"mission-control", "bridge":
		return PairingPlanned
	default:
		return PairingUnknown
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

// Delivery is how stream entries travel between this instance and a peer
// (decision 0021, Constraints): an instance behind NAT cannot be pushed to,
// so it pulls instead, and the instance it pulls from must not push to it.
// One instance of the pair can reach the other, so the two records of one
// pair differ: the unreachable instance records the other as DeliveryPull
// and is recorded by it as DeliveryPulled.
type Delivery string

// Deliveries. The zero value is DeliveryPush.
const (
	// DeliveryPush: each instance pushes its own entries to the other.
	DeliveryPush Delivery = "push"
	// DeliveryPull: this instance pulls the peer's entries, because the
	// peer cannot reach it; it still pushes its own entries to the peer.
	DeliveryPull Delivery = "pull"
	// DeliveryPulled: the peer pulls this instance's entries, because this
	// instance cannot reach it, so this instance pushes nothing to it.
	DeliveryPulled Delivery = "pulled"
)

// KnownDelivery reports whether d is one of the deliveries, the zero value
// included.
func KnownDelivery(d Delivery) bool {
	switch d {
	case "", DeliveryPush, DeliveryPull, DeliveryPulled:
		return true
	default:
		return false
	}
}

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
	// Delivery is how stream entries travel to and from the peer; the
	// stores return DeliveryPush for a peer recorded without one.
	Delivery Delivery
}

// Pushes reports whether this instance pushes its entries to the peer.
func (p Peer) Pushes() bool { return p.Delivery != DeliveryPulled }

// Pulls reports whether this instance pulls the peer's entries.
func (p Peer) Pulls() bool { return p.Delivery == DeliveryPull }

// Has reports whether the peer is paired for u.
func (p Peer) Has(u UseCase) bool { return slices.Contains(p.UseCases, u) }

// Compatible reports whether the peer's recorded major protocol version is
// this build's. A verified request with another major version is refused
// and recorded (decision 0021, Versioning), so the peer shows unavailable
// until it speaks this build's version again.
func (p Peer) Compatible() bool { return p.ProtocolVersion == instance.ProtocolVersion }

// DeriveStatus computes the JPD status: UNAUTHORIZED when the last signed
// exchange was rejected, OFFLINE when its protocol major version is not
// this build's or nothing succeeded within the stale window, PARTIAL when
// some use cases are failing, ONLINE otherwise.
func (p Peer) DeriveStatus(
	now time.Time,
	stale time.Duration,
	rejected bool,
	failingUseCases int,
) PeerStatus {
	switch {
	case rejected:
		return PeerUnauthorized
	case !p.Compatible(), p.LastSeen.IsZero() || now.Sub(p.LastSeen) > stale:
		return PeerOffline
	case failingUseCases > 0:
		return PeerPartial
	default:
		return PeerOnline
	}
}

// PairingNonce backs a single-use pairing token. Only the nonce hash is
// stored until the token is redeemed.
//
// RedeemedBy names who redeemed it, the redeeming instance and the
// fingerprint of the key it presented, so that a repeat of a redeem that
// succeeded can be answered again while the token lasts (the peer API's
// POST /pair is natural on the nonce) and any other redeemer refused.
type PairingNonce struct {
	NonceHash  []byte
	UseCase    UseCase
	ExpiresAt  time.Time
	RedeemedAt time.Time
	RedeemedBy string
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
	Time   outbox.HLC
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

// StreamResync records that a receiver's position in an origin's stream is
// below the origin's pruned floor: the entries after it are gone from the
// origin's outbox, so the receiver can catch up only through a full sync
// (decision 0021, Ordered outbox; PEER-05). The origin records it for a
// peer it cannot send to any more, and the receiver for an origin that
// answered its pull with 410; one of Origin and Receiver is this instance.
type StreamResync struct {
	Origin   string
	Receiver string
	Stream   string
	// Position is the receiver's position when the gap was found: the
	// highest sequence it had applied, or the origin had seen acknowledged.
	Position int64
	Flagged  time.Time
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

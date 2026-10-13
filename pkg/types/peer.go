package types

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidInstanceID reports an instance id that is not jfrt@<uuid>.
var ErrInvalidInstanceID = errors.New("invalid instance id")

// ErrInvalidStream reports a stream name with an unknown form.
var ErrInvalidStream = errors.New("invalid stream name")

// ErrInvalidHLC reports a hybrid logical clock value that does not parse.
var ErrInvalidHLC = errors.New("invalid hybrid logical clock value")

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

// HLC is a hybrid logical clock timestamp (Kulkarni et al., 2014): physical
// milliseconds plus a logical counter. It orders events across instances
// whose wall clocks disagree, which last-writer-wins needs (decision 0021).
type HLC struct {
	Wall    int64 // Unix milliseconds
	Logical uint32
}

// Compare orders two timestamps.
func (h HLC) Compare(o HLC) int {
	switch {
	case h.Wall != o.Wall:
		return sign64(h.Wall - o.Wall)
	case h.Logical != o.Logical:
		if h.Logical < o.Logical {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// sign64 is only called with a non-zero difference.
func sign64(n int64) int {
	if n < 0 {
		return -1
	}
	return 1
}

// Next is the timestamp of a local event after h at physical time now.
func (h HLC) Next(now time.Time) HLC {
	pt := now.UnixMilli()
	if pt > h.Wall {
		return HLC{Wall: pt}
	}
	return HLC{Wall: h.Wall, Logical: h.Logical + 1}
}

// Merge is the timestamp of receiving remote at physical time now, so the
// local clock never runs behind anything it has seen.
func (h HLC) Merge(remote HLC, now time.Time) HLC {
	pt := now.UnixMilli()
	wall := max(h.Wall, remote.Wall, pt)
	switch {
	case wall == h.Wall && wall == remote.Wall:
		return HLC{Wall: wall, Logical: max(h.Logical, remote.Logical) + 1}
	case wall == h.Wall:
		return HLC{Wall: wall, Logical: h.Logical + 1}
	case wall == remote.Wall:
		return HLC{Wall: wall, Logical: remote.Logical + 1}
	default:
		return HLC{Wall: wall}
	}
}

// String renders the timestamp as fixed-width hex, so the text form sorts the
// same way Compare does and can be stored in an indexed column.
func (h HLC) String() string { return fmt.Sprintf("%016x.%08x", uint64(h.Wall), h.Logical) }

// ParseHLC reads the String form.
func ParseHLC(s string) (HLC, error) {
	wall, logical, ok := strings.Cut(s, ".")
	if !ok || len(wall) != 16 || len(logical) != 8 {
		return HLC{}, fmt.Errorf("%w: %q", ErrInvalidHLC, s)
	}
	w, err := strconv.ParseUint(wall, 16, 64)
	if err != nil {
		return HLC{}, fmt.Errorf("%w: %q", ErrInvalidHLC, s)
	}
	l, err := strconv.ParseUint(logical, 16, 32)
	if err != nil {
		return HLC{}, fmt.Errorf("%w: %q", ErrInvalidHLC, s)
	}
	return HLC{Wall: int64(w), Logical: uint32(l)}, nil
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

// OutboxEntry is one change queued for peers, written in the same
// transaction as the change it records. (Origin, Stream, Seq) is unique and
// Seq is gap-free per stream, which lets a receiver detect a missing batch.
// BlobSHA256 is set for content events; receivers pull the blob.
type OutboxEntry struct {
	Origin     string
	Stream     string
	Seq        int64
	Time       HLC
	EventType  string
	Payload    []byte
	BlobSHA256 string
	BlobSize   int64
	Created    time.Time
}

// PeerCursor is the highest sequence a peer has acknowledged on a stream.
type PeerCursor struct {
	PeerInstanceID string
	Stream         string
	AckedSeq       int64
	LastSent       time.Time
	LastError      string
}

// Behind is how many entries the peer still has to apply, given the
// stream's head sequence.
func (c PeerCursor) Behind(head int64) int64 { return max(head-c.AckedSeq, 0) }

// AppliedMarker is the receiver's record of the last sequence applied from
// one origin's stream, updated in the apply transaction so a retried batch is
// idempotent.
type AppliedMarker struct {
	Origin  string
	Stream  string
	LastSeq int64
}

// Expect is the sequence the next batch must start at.
func (m AppliedMarker) Expect() int64 { return m.LastSeq + 1 }

// FailedBlobFetch records content a receiver could not fetch from a peer; it
// backs the federation failed-binary APIs and is retried with back-off.
type FailedBlobFetch struct {
	Stream      string
	SHA256      string
	SHA1        string
	RepoKey     string
	FirstFailed time.Time
	Failures    int
	LastError   string
}

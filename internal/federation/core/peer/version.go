package peer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/instance"
)

// HeaderProtocol carries the sender's protocol version on every peer request
// and answer (decision 0021, Versioning).
const HeaderProtocol = "Artifactweave-Peer-Protocol"

// ErrVersion reports an Artifactweave-Peer-Protocol value that is missing or
// not <major>.<minor>.
var ErrVersion = errors.New("the peer protocol version is not <major>.<minor>")

// Version is a peer protocol version. Minor versions are additive, so only
// the major version decides whether two instances can talk.
type Version struct {
	Major int
	Minor int
}

// CurrentVersion is the version this build speaks: instance.ProtocolVersion
// is its major version, the one a peer record stores, and api/src/peer's
// info.version is the whole of it.
var CurrentVersion = Version{Major: instance.ProtocolVersion, Minor: 0}

func (v Version) String() string { return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) }

// Compatible reports whether a peer speaking v can talk to this build.
func (v Version) Compatible() bool { return v.Major == CurrentVersion.Major }

// ParseVersion reads an Artifactweave-Peer-Protocol value.
func ParseVersion(s string) (Version, error) {
	major, minor, ok := strings.Cut(s, ".")
	a, errA := number(major)
	b, errB := number(minor)
	if !ok || errA != nil || errB != nil {
		return Version{}, fmt.Errorf("%w: %q", ErrVersion, s)
	}
	return Version{Major: a, Minor: b}, nil
}

// number reads digits only: strconv.Atoi alone would take a sign.
func number(s string) (int, error) {
	if s == "" || len(s) > 9 || strings.Trim(s, "0123456789") != "" {
		return 0, ErrVersion
	}
	return strconv.Atoi(s) //nolint:wrapcheck // the caller wraps ErrVersion
}

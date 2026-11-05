package types

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidHLC reports a hybrid logical clock value that does not parse.
var ErrInvalidHLC = errors.New("invalid hybrid logical clock value")

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

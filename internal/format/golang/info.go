package golang

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// MaxInfo is the largest .info a Go deploy may carry: 64 KiB, far above
// the two fields the go command reads.
const MaxInfo = 64 << 10

// CheckInfo applies decision 0035's check to the body of a .info deployed
// through the Go API: a JSON object of at most MaxInfo bytes whose Version
// is f's version and whose Time, when present, is RFC 3339, so the go
// command can read what @latest and .info serve. The fields are matched as
// the GOPROXY protocol spells them.
func CheckInfo(f File, body []byte) error {
	invalid := func(why string) error {
		return &Invalid{Message: fmt.Sprintf(
			"The .info of version %s of module %s %s.", f.Version, f.Module, why)}
	}
	if len(body) > MaxInfo {
		return invalid("is larger than 64 KiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return invalid("is not a JSON object")
	}
	var v string
	if raw, ok := fields["Version"]; !ok || json.Unmarshal(raw, &v) != nil || v != f.Version {
		return invalid("does not give its Version as " + f.Version)
	}
	var t *string
	if raw, ok := fields["Time"]; ok {
		if json.Unmarshal(raw, &t) != nil {
			return invalid("gives a Time that is not a string")
		}
	}
	if t != nil {
		if _, err := time.Parse(time.RFC3339, *t); err != nil {
			return invalid("gives a Time that is not RFC 3339")
		}
	}
	return nil
}

// infoTime reads the Time a stored .info gives. ok is false when it gives
// none the go command could read: the item routes store a .info unchecked,
// and @v/list then dates the version as a synthesised .info would.
func infoTime(body []byte) (t time.Time, ok bool) {
	var info struct {
		Time *string `json:"Time"` //nolint:tagliatelle // the GOPROXY protocol's name
	}
	if len(body) > MaxInfo || json.Unmarshal(body, &info) != nil || info.Time == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, *info.Time)
	return t, err == nil
}

// storedTime reads the Time of the .info stored as it. Without a blob
// store to read, as in a hook run on a store alone, it reads none.
func storedTime(ctx context.Context, blobs format.Blobs, it types.Item) (time.Time, bool, error) {
	if blobs == nil {
		return time.Time{}, false, nil
	}
	rc, _, err := blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("golang: open %s: %w", it.FullPath(), err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, MaxInfo+1))
	if err != nil {
		return time.Time{}, false, fmt.Errorf("golang: read %s: %w", it.FullPath(), err)
	}
	t, ok := infoTime(body)
	return t, ok, nil
}

// versionMeta is a version's metadata in the package index: the time its
// stored .info gives, which @v/list answers beside it.
type versionMeta struct {
	Time string `json:"time"`
}

// metadataOf is the index metadata of a version whose .info gives t; none
// when ok is false.
func metadataOf(t time.Time, ok bool) []byte {
	if !ok {
		return nil
	}
	// Marshalling a struct of one string cannot fail.
	b, _ := json.Marshal(versionMeta{Time: t.UTC().Format(time.RFC3339)})
	return b
}

// ListTime is the time @v/list gives version v: its stored .info's Time,
// or else when its first file was stored, which a synthesised .info gives.
func ListTime(v types.PackageVersion) time.Time {
	var m versionMeta
	if json.Unmarshal(v.Metadata, &m) == nil {
		if t, err := time.Parse(time.RFC3339, m.Time); err == nil {
			return t
		}
	}
	return v.Created
}

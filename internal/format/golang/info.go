package golang

import (
	"encoding/json"
	"fmt"
	"time"
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

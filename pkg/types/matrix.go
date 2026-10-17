package types

import "slices"

// Matrix is a download's matrix parameters: the values asked for per key,
// and the keys sent as "key+=value", which the item must carry.
type Matrix struct {
	Props     Properties
	Mandatory map[string]bool
}

// Matches reports whether a file carrying props serves a download that
// asks for m: a key the file carries must have one of the values asked for,
// and a mandatory key must be carried. A key the file lacks does not stop a
// match otherwise.
func (m Matrix) Matches(props Properties) bool {
	for k, want := range m.Props {
		have, ok := props[k]
		if !ok {
			if m.Mandatory[k] {
				return false
			}
			continue
		}
		if !slices.ContainsFunc(want, func(w string) bool { return slices.Contains(have, w) }) {
			return false
		}
	}
	return true
}

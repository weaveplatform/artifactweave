// Package u is a fixture of the seal scan: calls whose purpose it cannot
// read.
package u

import "net/http"

var purposeVar = "u.var"

type store struct{}

// Seal has the store's shape.
func (store) Seal(purpose, rowID, value string) ([]byte, error) {
	return []byte(purpose + rowID + value), nil
}

func purposeFor(kind string) string { return "u." + kind }

func put(s store, kind, value string) {
	_, _ = s.Seal(purposeFor(kind), "row", value)
	_, _ = s.Seal(purposeVar, "row", value)
	_, _ = s.Seal(kind+".suffix", "row", value)
	_, _ = s.Seal(http.MethodGet, "row", value)
	local := "u.local"
	_, _ = s.Seal(local, "row", value)
}

// shadow declares its parameter again, so the purpose is not the one passed
// in.
func shadow(s store, purpose string) {
	if purpose != "" {
		purpose := purpose + ".x"
		_, _ = s.Seal(purpose, "row", "value")
	}
}

// Package keys is a fixture of the seal scan: a store that seals under
// constants and a literal, and wrappers that pass their purpose on.
package keys

const purposeKey = "keys.private_key"

const (
	purposeFirst = "keys.first"
	// purposeRepeat repeats the value above, as a const block allows.
	purposeRepeat
)

type ring struct{}

// Seal has the keyring's shape: a purpose, a row id and a value.
func (ring) Seal(purpose, rowID string, plain []byte) ([]byte, error) {
	return append([]byte(purpose+rowID), plain...), nil
}

type store struct{ keys ring }

// Seal passes its purpose on, so the scan skips its call and reads the
// calls of Seal instead.
func (s store) Seal(purpose, rowID, plain string) ([]byte, error) {
	return s.keys.Seal(purpose, rowID, []byte(plain))
}

func (s store) put(name, value string) {
	_, _ = s.Seal(purposeKey, name, value)
	_, _ = s.Seal(purposeRepeat, name, value)
	_, _ = s.Seal(purposeElsewhere, name, value)
	_, _ = s.Seal("keys.literal", name, value)
	func() { _, _ = s.keys.Seal(purposeKey, name, nil) }()
}

// sealWith passes its purpose on from a function literal.
var sealWith = func(s store, purpose string) { _, _ = s.Seal(purpose, "row", "value") }

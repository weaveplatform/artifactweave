package sqlcommon

// SealedPurposes exposes the purpose of every sealed column the modules
// register, which Reseal covers, to the external tests, in registration
// order and with any repeat kept.
func SealedPurposes() []string {
	var out []string
	for _, m := range modules() {
		for _, c := range m.Sealed {
			out = append(out, c.Purpose)
		}
	}
	return out
}

package sqlcommon

// SealedPurposes exposes the purposes Reseal covers to the external tests.
func SealedPurposes() []string {
	out := make([]string, 0, len(sealedColumns))
	for _, c := range sealedColumns {
		out = append(out, c.purpose)
	}
	return out
}

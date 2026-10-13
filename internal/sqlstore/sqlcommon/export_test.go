package sqlcommon

// SealedPurposes exposes the purposes Reseal covers to the external tests.
func SealedPurposes() []string {
	out := make([]string, 0, len(sealedColumns))
	for _, c := range sealedColumns {
		out = append(out, c.purpose)
	}
	return out
}

// MustJSON exposes mustJSON so its failure mode can be tested.
var MustJSON = mustJSON

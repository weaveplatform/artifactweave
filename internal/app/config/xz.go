package config

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// DefaultXZDictionaryBudget is xzfile.DefaultBudget, 1 GiB: one decode of
// a block that names up to 1 GiB, or a hundred and twenty-eight of
// xz -6's 8 MiB, hold their dictionaries at once.
const DefaultXZDictionaryBudget = ByteSize(xzfile.DefaultBudget)

// XZ holds the xz decoder's options (#1354).
type XZ struct {
	// DictionaryBudget is the most bytes of LZMA2 dictionary the process's
	// xz decodes hold at once (xz.dictionary_budget); 0 turns the budget
	// off. A decode that does not fit waits and is never refused, so it
	// changes no answer.
	DictionaryBudget ByteSize `yaml:"dictionary_budget"`
}

// setXZ assigns one key under xz; sub is the key without that prefix.
func setXZ(x *XZ, sub, key, value string) error {
	if sub != "dictionary_budget" {
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return setByteSize(&x.DictionaryBudget, key, value)
}

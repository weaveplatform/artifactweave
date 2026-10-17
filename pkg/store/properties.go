package store

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// CleanProperties is how the stores keep properties, one row per value:
// keys without values dropped, each value cut to limit
// (types.TruncatePropertyValue), and the values sorted and unique, so two
// values that differ only past the limit become one.
func CleanProperties(props types.Properties, limit int) types.Properties {
	out := types.Properties{}
	for k, values := range props {
		if len(values) == 0 {
			continue
		}
		cut := make([]string, len(values))
		for i, v := range values {
			cut[i] = types.TruncatePropertyValue(v, limit)
		}
		out.Set(k, cut...)
	}
	return out
}

// CheckPropertyKeys returns ErrInvalid, wrapping types.ErrPropertyKeyTooLong,
// when a key of props is longer than types.PropertyKeyMax. Every store
// checks before it writes, so a property write either stores every key or
// none, whichever route it came by.
func CheckPropertyKeys(props types.Properties) error {
	for k := range props {
		if err := types.CheckPropertyKeyLength(k); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return nil
}

// CheckBuildPropertyKeys is CheckPropertyKeys over b's build and module
// properties.
func CheckBuildPropertyKeys(b types.Build) error {
	if err := CheckPropertyKeys(b.Properties); err != nil {
		return err
	}
	for _, m := range b.Modules {
		if err := CheckPropertyKeys(m.Properties); err != nil {
			return err
		}
	}
	return nil
}

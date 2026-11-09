package port

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// CleanProperties is how the stores keep properties, one row per value:
// keys without values dropped, each value cut to limit
// (types.TruncatePropertyValue), and the values sorted and unique, so two
// values that differ only past the limit become one.
func CleanProperties(props model.Properties, limit int) model.Properties {
	out := model.Properties{}
	for k, values := range props {
		if len(values) == 0 {
			continue
		}
		cut := make([]string, len(values))
		for i, v := range values {
			cut[i] = model.TruncatePropertyValue(v, limit)
		}
		out.Set(k, cut...)
	}
	return out
}

// CheckPropertyKeys returns ErrInvalid, wrapping types.ErrPropertyKeyTooLong,
// when a key of props is longer than types.PropertyKeyMax. Every store
// checks before it writes, so a property write either stores every key or
// none, whichever route it came by.
func CheckPropertyKeys(props model.Properties) error {
	for k := range props {
		if err := model.CheckPropertyKeyLength(k); err != nil {
			return fmt.Errorf("%w: %w", kernel.ErrInvalid, err)
		}
	}
	return nil
}

// CheckBuildPropertyKeys is CheckPropertyKeys over b's build and module
// properties.
func CheckBuildPropertyKeys(b model.Build) error {
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

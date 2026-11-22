package descriptor

import (
	"fmt"
	"maps"
	"slices"
)

// Validate the merged document before planning any writes, including secrets.
// Nulls in nested property definitions retain the descriptor's clearing form;
// top-level null fields have already been removed by mergeConfig.
func validateProxyFields(doc map[string]any) error {
	return configFieldTypes(doc,
		[]string{"username", "domain", "ntHost", "redirectedToHosts", "services"},
		[]string{"platformDefault"})
}

func validatePropertySetFields(doc map[string]any) error {
	if err := configFieldTypes(doc, nil, []string{"visible"}); err != nil {
		return err
	}
	v, present := doc["properties"]
	if !present {
		return nil
	}
	props, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: properties must be a mapping of property names", ErrInvalid)
	}
	for _, name := range slices.Sorted(maps.Keys(props)) {
		if props[name] == nil {
			continue
		}
		fields, ok := props[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: property %s must be a mapping", ErrInvalid, name)
		}
		if err := validatePropertyFields(fields); err != nil {
			return fmt.Errorf("properties.%s: %w", name, err)
		}
	}
	return nil
}

func validatePropertyFields(fields map[string]any) error {
	if bad := unknown(fields, []string{"closedPredefinedValues", "multipleChoice", "predefinedValues"}); bad != "" {
		return fmt.Errorf("%w: unknown field %s", ErrInvalid, bad)
	}
	if err := configFieldTypes(fields, nil, []string{"closedPredefinedValues", "multipleChoice"}); err != nil {
		return err
	}
	switch values := fields["predefinedValues"].(type) {
	case nil:
		return nil
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(values)) {
			if values[name] == nil {
				continue
			}
			fields, ok := values[name].(map[string]any)
			if !ok {
				return fmt.Errorf("%w: predefinedValues.%s must be a mapping or null", ErrInvalid, name)
			}
			if err := validatePredefinedValue(fields, false); err != nil {
				return fmt.Errorf("predefinedValues.%s: %w", name, err)
			}
		}
	case []any:
		for i, value := range values {
			switch fields := value.(type) {
			case string:
			case map[string]any:
				if err := validatePredefinedValue(fields, true); err != nil {
					return fmt.Errorf("predefinedValues[%d]: %w", i, err)
				}
			default:
				return fmt.Errorf("%w: predefinedValues[%d] must be a string or mapping", ErrInvalid, i)
			}
		}
	default:
		return fmt.Errorf("%w: predefinedValues must be a mapping or list", ErrInvalid)
	}
	return nil
}

func validatePredefinedValue(fields map[string]any, list bool) error {
	allowed := []string{"defaultValue"}
	var stringFields []string
	if list {
		allowed = append(allowed, "value")
		stringFields = []string{"value"}
	}
	if bad := unknown(fields, allowed); bad != "" {
		return fmt.Errorf("%w: unknown field %s", ErrInvalid, bad)
	}
	return configFieldTypes(fields, stringFields, []string{"defaultValue"})
}

func configFieldTypes(fields map[string]any, strings, booleans []string) error {
	for _, name := range strings {
		if value := fields[name]; value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%w: %s must be a string", ErrInvalid, name)
			}
		}
	}
	for _, name := range booleans {
		if value := fields[name]; value != nil {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%w: %s must be a boolean", ErrInvalid, name)
			}
		}
	}
	return nil
}

package repo

import "fmt"

// creationOnlyField is a field a repository of some package type takes only
// when it is created: value reads it from a configuration, so two
// configurations compare. When unset is not nil, a repository whose stored
// value is unset may still take one, once.
type creationOnlyField struct {
	field string
	value func(*Config) any
	unset any
}

// creationOnly are the fields, by package type, that an update may repeat
// but never change. Helm's Enforce Layout fields are "set only upon
// repository creation" (kubernetes-helm-chart-repositories), and the
// Terraform provider refuses a change to either in the plan ("Cannot be
// updated after it is set", local_helm_repository). What 7.x answers an
// update that changes one is [I] (#216); refusing it is decision 0039's
// choice, so a caller never reads a success for a change that did not
// happen. A Terraform repository's terraformType decides how every stored
// file is read, so it cannot change or be removed once set; a repository
// created without one may take one later, which reindexes it (decision
// 0040).
var creationOnly = map[PackageType][]creationOnlyField{
	Helm: {
		{
			"forceNonDuplicateChart",
			func(c *Config) any { return isOn(c.ForceNonDuplicateChart) },
			nil,
		},
		{
			"forceMetadataNameVersion",
			func(c *Config) any { return isOn(c.ForceMetadataNameVersion) },
			nil,
		},
	},
	Terraform: {
		{"terraformType", func(c *Config) any { return c.TerraformType }, TerraformType("")},
	},
}

// isOn reads a boolean field that is off unless set on.
func isOn(b *bool) bool { return b != nil && *b }

// CreationOnlyError is an update that changes a field its repository takes
// only when it is created, or, with Once, a field it may take once and then
// keep. It unwraps to ErrInvalid.
type CreationOnlyError struct {
	Field       string
	PackageType PackageType
	Once        bool
}

func (e CreationOnlyError) Error() string {
	name := PackageTypeName(string(e.PackageType))
	if e.Once {
		return fmt.Sprintf(
			"The field '%s' of a %s repository cannot change once set",
			e.Field,
			name,
		)
	}
	return fmt.Sprintf(
		"The field '%s' of a %s repository can be set only when the repository is created",
		e.Field,
		name,
	)
}

// Unwrap lets errors.Is(err, ErrInvalid) work.
func (CreationOnlyError) Unwrap() error { return ErrInvalid }

// ValidateUpdate checks updated, the configuration an update of stored
// leaves once merged, defaulted and validated, against the fields of
// stored's package type that only a create may set: a CreationOnlyError
// names the first one it changes. An update that leaves such a field out,
// repeats its stored value, or sets one the repository was created without
// where the field allows it, passes.
func ValidateUpdate(stored, updated *Config) error {
	for _, f := range creationOnly[stored.PackageType] {
		was := f.value(stored)
		if was == f.value(updated) || f.unset != nil && was == f.unset {
			continue
		}
		return CreationOnlyError{
			Field:       f.field,
			PackageType: stored.PackageType,
			Once:        f.unset != nil,
		}
	}
	return nil
}

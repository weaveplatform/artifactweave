package config

import (
	"fmt"
	"strconv"

	"github.com/weaveplatform/artifactweave/migration/platform/apilimit"
)

func setAPI(o *apilimit.Options, sub, key, value string) error {
	rates := map[string]*int64{
		"authentication_per_minute":      &o.AuthenticationPerMinute,
		"global_per_minute":              &o.GlobalPerMinute,
		"principal_per_minute":           &o.PrincipalPerMinute,
		"project_per_minute":             &o.ProjectPerMinute,
		"repository_per_minute":          &o.RepositoryPerMinute,
		"expensive_per_minute":           &o.ExpensivePerMinute,
		"expensive_principal_per_minute": &o.ExpensivePrincipalPerMinute,
	}
	if dst := rates[sub]; dst != nil {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %s must be an integer", ErrInvalidValue, key)
		}
		*dst = n
		return nil
	}
	switch sub {
	case "enabled":
		return setBool(&o.Enabled, key, value)
	case "max_concurrent":
		return setInt(&o.MaxConcurrent, key, value)
	case "expensive_concurrent":
		return setInt(&o.ExpensiveConcurrent, key, value)
	case "expensive_principal_concurrent":
		return setInt(&o.ExpensivePrincipalConcurrent, key, value)
	case "max_keys":
		return setInt(&o.MaxKeys, key, value)
	case "budget_timeout":
		return setDuration(&o.BudgetTimeout, key, value)
	case "expensive_timeout":
		return setDuration(&o.ExpensiveTimeout, key, value)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
}

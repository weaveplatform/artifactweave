package descriptor

import "fmt"

// parseSecurity accepts Artifactory's anonymous access switch and the LDAP
// connections (parseLDAP). Other security settings remain unsupported and
// fail closed.
func parseSecurity(top map[string]any, p *Patch) error {
	v, present := top[SectionSecurity]
	if !present {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: security must be a mapping", ErrInvalid)
	}
	if bad := unknown(m, []string{"anonAccessEnabled", ldapSection}); bad != "" {
		return fmt.Errorf("%w: security: unknown field %s", ErrInvalid, bad)
	}
	if value, present := m["anonAccessEnabled"]; present {
		enabled, ok := value.(bool)
		if !ok {
			return fmt.Errorf("%w: security.anonAccessEnabled must be a boolean", ErrInvalid)
		}
		p.AnonymousAccess = &enabled
	}
	return parseLDAP(m, p)
}

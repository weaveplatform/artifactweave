package ldap

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

// The Access API's names for the fields a refusal can name. The descriptor
// maps them to its own spelling (FieldNames), so both surfaces name the same
// rule.
const (
	FieldKey             = "key"
	FieldURL             = "ldap_url"
	FieldUserDNPattern   = "user_dn_pattern"
	FieldEmailAttribute  = "email_attribute"
	FieldSearchFilter    = "search.search_filter"
	FieldSearchBase      = "search.search_base"
	FieldManagerDN       = "search.manager_dn"
	FieldManagerPassword = "search.manager_password"
)

// invalid is a refusal naming one field; Field "" names the whole
// connection. Its text reaches clients, so it never carries a secret.
type invalid = accessapi.LDAPInvalid

func refuse(field, reason string) error { return &invalid{Field: field, Reason: reason} }

// usernameArgument is the placeholder JFrog documents for both the user DN
// pattern and the search filter: the login name replaces it at bind time.
const usernameArgument = "{0}"

// attributeName is an LDAP attribute description (RFC 4512 section 2.5): a
// descriptor or a numeric OID, with options such as ;lang-en.
var attributeName = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9-]*|[0-9]+(?:\.[0-9]+)+)(?:;[A-Za-z0-9-]+)*$`)

// validate checks a connection as it will be stored. hasPassword says whether
// a manager password will be stored with it. The rules are local engineering
// choices (research 05, "LDAP connections"): JFrog documents the fields but
// not their validation, and each rule refuses a value the directory client
// of LDAP-02 could never use, rather than storing it to fail at login.
func validate(c accessapi.LDAPConnection, hasPassword bool) error {
	switch {
	case c.Key == "":
		return refuse(FieldKey, "is required")
	case strings.Contains(c.Key, "/"):
		// GET and DELETE address a connection by one path segment.
		return refuse(FieldKey, "must not contain /")
	case strings.ContainsFunc(c.Key, unicode.IsControl):
		// The snapshot revision separates keys with NUL.
		return refuse(FieldKey, "must not contain control characters")
	}
	if err := validURL(c.URL); err != nil {
		return err
	}
	if c.UserDNPattern == "" && c.SearchFilter == "" {
		return refuse("", "needs user_dn_pattern or search.search_filter to find a user's entry")
	}
	if c.UserDNPattern != "" {
		if !strings.Contains(c.UserDNPattern, usernameArgument) {
			return refuse(FieldUserDNPattern, "must contain {0}, which the username replaces")
		}
		if !validDN(strings.ReplaceAll(c.UserDNPattern, usernameArgument, "x")) {
			return refuse(FieldUserDNPattern, "must be a distinguished name such as uid={0},ou=people")
		}
	}
	if c.SearchFilter != "" {
		if !strings.Contains(c.SearchFilter, usernameArgument) {
			return refuse(FieldSearchFilter, "must contain {0}, which the username replaces")
		}
		if !balanced(c.SearchFilter) {
			return refuse(FieldSearchFilter, "must have balanced parentheses, for example (uid={0})")
		}
	}
	if c.SearchBase != "" {
		// JFrog documents several bases separated by a pipe.
		for base := range strings.SplitSeq(c.SearchBase, "|") {
			if !validDN(base) {
				return refuse(FieldSearchBase, "must be distinguished names separated by |")
			}
		}
	}
	if c.ManagerDN != "" && !validDN(c.ManagerDN) {
		return refuse(FieldManagerDN, "must be a distinguished name")
	}
	if hasPassword && c.ManagerDN == "" {
		return refuse(FieldManagerPassword, "needs search.manager_dn")
	}
	if c.EmailAttribute != "" && !attributeName.MatchString(c.EmailAttribute) {
		return refuse(FieldEmailAttribute, "must be an LDAP attribute name such as mail")
	}
	return nil
}

func validURL(raw string) error {
	if raw == "" {
		return refuse(FieldURL, "is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (!strings.EqualFold(u.Scheme, "ldap") && !strings.EqualFold(u.Scheme, "ldaps")) ||
		u.Host == "" || u.Hostname() == "" {
		return refuse(FieldURL, "must be an ldap:// or ldaps:// URL with a host, such as ldap://ldap.example.com:389/dc=example,dc=com")
	}
	if u.User != nil {
		// The URL is returned and exported; a credential in it would escape
		// the sealed manager password.
		return refuse(FieldURL, "must not carry credentials; use search.manager_dn and search.manager_password")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return refuse(FieldURL, "must not have a query or fragment; the path is the base DN")
	}
	if base := strings.TrimPrefix(u.Path, "/"); base != "" && !validDN(base) {
		return refuse(FieldURL, "must name its base DN as a distinguished name, such as dc=example,dc=com")
	}
	return nil
}

// validDN reports whether s is a distinguished name in the string form of
// RFC 4514: relative names separated by commas, each one or more
// type=value pairs joined by +, where a backslash escapes the next
// character. Values are not interpreted further; the directory does that.
func validDN(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, rdn := range splitUnescaped(s, ',') {
		if rdn == nil {
			return false
		}
		for _, ava := range splitUnescaped(*rdn, '+') {
			if ava == nil {
				return false
			}
			typ, _, ok := strings.Cut(*ava, "=")
			if !ok || !attributeName.MatchString(strings.TrimSpace(typ)) {
				return false
			}
		}
	}
	return true
}

// splitUnescaped splits s at each sep that no backslash escapes. A trailing
// backslash, which escapes nothing, yields a nil part.
func splitUnescaped(s string, sep byte) []*string {
	var parts []*string
	start, escaped := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == sep:
			part := s[start:i]
			parts = append(parts, &part)
			start = i + 1
		}
	}
	if escaped {
		return append(parts, nil)
	}
	last := s[start:]
	return append(parts, &last)
}

// balanced reports whether the parentheses of an LDAP filter nest. A filter
// without them, such as sAMAccountName={0}, which JFrog's Entra ID example
// uses, balances.
func balanced(filter string) bool {
	depth, escaped := 0, false
	for i := 0; i < len(filter); i++ {
		switch {
		case escaped:
			escaped = false
		case filter[i] == '\\':
			escaped = true
		case filter[i] == '(':
			depth++
		case filter[i] == ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !escaped
}

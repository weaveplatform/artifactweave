package ldap

import (
	"context"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
)

// Bounds on what one group call reads, so a directory with an unexpected
// shape costs a bounded amount and fails rather than growing without end.
// JFrog documents none [I].
const (
	// MaxGroups is the most groups one search returns; a larger answer is
	// a *DirectoryError, since a partial list would import a partial set.
	MaxGroups = 10_000
	// MaxMemberValues is the most group values a DYNAMIC user entry is
	// read for; the rest are left out, as a malformed directory's are.
	MaxMemberValues = 1_000
)

// open dials c, binds as its manager or stays anonymous, and returns the
// session and the URL's target; the caller closes the session.
func (c Client) open(ctx context.Context, conn coreldap.Connection) (session, target, error) {
	t, err := parseTarget(conn.URL)
	if err != nil {
		return session{}, target{}, err
	}
	s, err := c.dial(ctx, t)
	if err != nil {
		return session{}, target{}, err
	}
	if err := c.manager(s, conn); err != nil {
		s.close()
		return session{}, target{}, err
	}
	return s, t, nil
}

// Lookup implements Directory: the user DN pattern's entry when it exists,
// otherwise the search's single entry, read as the manager. Poisoning
// protection refuses an entry as Authenticate does.
func (c Client) Lookup(ctx context.Context, conn coreldap.Connection, name string) (coreldap.Entry, error) {
	s, t, err := c.open(ctx, conn)
	if err != nil {
		return coreldap.Entry{}, err
	}
	defer s.close()
	if conn.UserDNPattern != "" {
		dn := under(strings.ReplaceAll(conn.UserDNPattern, "{0}", goldap.EscapeDN(name)), t.baseDN)
		entries, err := c.search(s, conn, goldap.NewSearchRequest(
			dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 1, int(c.timeout()/time.Second), false,
			"(objectClass=*)", attributes(conn), nil,
		))
		switch {
		case err == nil && len(entries) == 1:
			if poisoned(conn, entries[0]) {
				return coreldap.Entry{}, coreldap.ErrUnknownUser
			}
			return coreldap.Entry{DN: entries[0].DN}, nil
		case err != nil && !goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
			return coreldap.Entry{}, s.failure("search", err)
		}
	}
	if conn.SearchFilter == "" {
		return coreldap.Entry{}, coreldap.ErrUnknownUser
	}
	found, err := c.find(s, conn, t, name)
	if err != nil {
		return coreldap.Entry{}, err
	}
	return coreldap.Entry{DN: found.DN}, nil
}

// filterOf is a setting's filter with the parentheses the protocol needs,
// selecting every entry when it has none.
func filterOf(f string) string {
	f = strings.TrimSpace(f)
	switch {
	case f == "":
		return "(objectClass=*)"
	case !strings.HasPrefix(f, "("):
		return "(" + f + ")"
	}
	return f
}

func groupScope(g accessapi.LDAPGroupSetting) int {
	if g.SubTree {
		return goldap.ScopeWholeSubtree
	}
	return goldap.ScopeSingleLevel
}

func groupAttributes(g accessapi.LDAPGroupSetting) []string {
	out := []string{g.GroupNameAttribute}
	if g.DescriptionAttribute != "" {
		out = append(out, g.DescriptionAttribute)
	}
	return out
}

// groupOf reads a group entry. An entry without the name attribute is no
// group, except under HIERARCHICAL, whose groups are the entries a DN names
// and so are named by their own RDN when the attribute is missing.
func groupOf(g accessapi.LDAPGroupSetting, e *goldap.Entry) (coreldap.DirectoryGroup, bool) {
	name := e.GetEqualFoldAttributeValue(g.GroupNameAttribute)
	if name == "" && g.Strategy == accessapi.LDAPHierarchical {
		if dn, err := goldap.ParseDN(e.DN); err == nil && len(dn.RDNs) > 0 && len(dn.RDNs[0].Attributes) > 0 {
			name = dn.RDNs[0].Attributes[0].Value
		}
	}
	if strings.TrimSpace(name) == "" {
		return coreldap.DirectoryGroup{}, false
	}
	out := coreldap.DirectoryGroup{Name: name, DN: e.DN}
	if g.DescriptionAttribute != "" {
		out.Description = e.GetEqualFoldAttributeValue(g.DescriptionAttribute)
	}
	return out, true
}

// searchGroups runs one group search under the setting's base DN.
func (c Client) searchGroups(
	s session,
	conn coreldap.Connection,
	t target,
	g accessapi.LDAPGroupSetting,
	filter string,
) ([]coreldap.DirectoryGroup, error) {
	entries, err := c.search(s, conn, goldap.NewSearchRequest(
		under(g.GroupBaseDN, t.baseDN), groupScope(g), goldap.NeverDerefAliases, MaxGroups+1,
		int(c.timeout()/time.Second), false, filter, groupAttributes(g), nil,
	))
	switch {
	case goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
		return nil, nil
	case goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded) || len(entries) > MaxGroups:
		return nil, &coreldap.DirectoryError{Op: "search", Reason: "the group search found more than 10000 groups"}
	case goldap.IsErrorWithCode(err, goldap.LDAPResultFilterError):
		return nil, &coreldap.DirectoryError{
			Op: "search", Reason: "the directory refused the group filter: " + err.Error(), Err: err,
		}
	case err != nil:
		return nil, s.failure("search", err)
	}
	out := make([]coreldap.DirectoryGroup, 0, len(entries))
	for _, e := range entries {
		if grp, ok := groupOf(g, e); ok {
			out = append(out, grp)
		}
	}
	return out, nil
}

// Groups implements Directory.
func (c Client) Groups(
	ctx context.Context,
	conn coreldap.Connection,
	g accessapi.LDAPGroupSetting,
) ([]coreldap.DirectoryGroup, error) {
	s, t, err := c.open(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer s.close()
	return c.searchGroups(s, conn, t, g, filterOf(g.Filter))
}

// GroupsNamed implements Directory. The name goes to the directory in the
// filter rather than being compared here, so the attribute's own matching
// rule decides, as JFrog's Create a Group page says it does. Under
// HIERARCHICAL a group without the name attribute is named by its RDN,
// whose attribute is the member attribute (groupOf), so that matches too.
func (c Client) GroupsNamed(
	ctx context.Context,
	conn coreldap.Connection,
	g accessapi.LDAPGroupSetting,
	name string,
) ([]coreldap.DirectoryGroup, error) {
	s, t, err := c.open(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer s.close()
	match := "(" + g.GroupNameAttribute + "=" + goldap.EscapeFilter(name) + ")"
	if g.Strategy == accessapi.LDAPHierarchical && g.GroupMemberAttribute != "" &&
		!strings.EqualFold(g.GroupMemberAttribute, g.GroupNameAttribute) {
		match = "(|" + match + "(" + g.GroupMemberAttribute + "=" + goldap.EscapeFilter(name) + "))"
	}
	return c.searchGroups(s, conn, t, g, "(&"+filterOf(g.Filter)+match+")")
}

// UserGroups implements Directory, by strategy as JFrog's LDAP page
// describes them:
//
//   - STATIC: the groups under the base whose member attribute holds the
//     user's DN or name ("user DNs or IDs of the group members").
//   - DYNAMIC: the groups the user entry's member attribute names, by DN or
//     by name, that the setting's filter selects under its base.
//   - HIERARCHICAL: the RDNs of the user's DN, between the entry and the
//     URL's base, whose attribute is the member attribute: uid=user1,
//     ou=developers,ou=uk names developers and uk.
//
// None reads one group to find another, so nesting and cycles cost nothing.
func (c Client) UserGroups(
	ctx context.Context,
	conn coreldap.Connection,
	g accessapi.LDAPGroupSetting,
	name, dn string,
) ([]coreldap.DirectoryGroup, error) {
	if g.GroupMemberAttribute == "" {
		return nil, nil
	}
	if g.Strategy == accessapi.LDAPHierarchical {
		t, err := parseTarget(conn.URL)
		if err != nil {
			return nil, err
		}
		return hierarchy(g, dn, t.baseDN), nil
	}
	s, t, err := c.open(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if g.Strategy == accessapi.LDAPDynamic {
		return c.dynamic(s, conn, t, g, dn)
	}
	member := g.GroupMemberAttribute
	filter := "(&" + filterOf(g.Filter) + "(|(" + member + "=" + goldap.EscapeFilter(dn) + ")(" +
		member + "=" + goldap.EscapeFilter(name) + ")))"
	return c.searchGroups(s, conn, t, g, filter)
}

// dynamic reads the user entry's member attribute and resolves each value:
// a DN is read as a group entry the filter must select under the base; any
// other value is a group name the search must find. A value that is
// neither is left out.
func (c Client) dynamic(
	s session,
	conn coreldap.Connection,
	t target,
	g accessapi.LDAPGroupSetting,
	dn string,
) ([]coreldap.DirectoryGroup, error) {
	entries, err := c.search(s, conn, goldap.NewSearchRequest(
		dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 1, int(c.timeout()/time.Second), false,
		"(objectClass=*)", []string{g.GroupMemberAttribute}, nil,
	))
	switch {
	case goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
		return nil, nil
	case err != nil:
		return nil, s.failure("read", err)
	case len(entries) != 1:
		return nil, nil
	}
	values := entries[0].GetEqualFoldAttributeValues(g.GroupMemberAttribute)
	if len(values) > MaxMemberValues {
		values = values[:MaxMemberValues]
	}
	base, err := goldap.ParseDN(under(g.GroupBaseDN, t.baseDN))
	if err != nil {
		base = nil
	}
	var out []coreldap.DirectoryGroup
	for _, v := range values {
		groupDN, err := goldap.ParseDN(v)
		if err != nil || len(groupDN.RDNs) == 0 {
			found, err := c.searchGroups(s, conn, t, g,
				"(&"+filterOf(g.Filter)+"("+g.GroupNameAttribute+"="+goldap.EscapeFilter(v)+"))")
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
			continue
		}
		if base != nil && len(base.RDNs) > 0 && !base.AncestorOfFold(groupDN) {
			continue
		}
		found, err := c.search(s, conn, goldap.NewSearchRequest(
			v, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 1, int(c.timeout()/time.Second), false,
			filterOf(g.Filter), groupAttributes(g), nil,
		))
		switch {
		case goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
			continue
		case err != nil:
			return nil, s.failure("read", err)
		}
		for _, e := range found {
			if grp, ok := groupOf(g, e); ok {
				out = append(out, grp)
			}
		}
	}
	return out, nil
}

// hierarchy reads the groups a DN names. RDNs at or above the URL's base DN
// belong to the directory, not to the user's groups.
func hierarchy(g accessapi.LDAPGroupSetting, dn, baseDN string) []coreldap.DirectoryGroup {
	parsed, err := goldap.ParseDN(dn)
	if err != nil {
		return nil
	}
	limit := len(parsed.RDNs)
	if base, err := goldap.ParseDN(baseDN); err == nil && len(base.RDNs) > 0 && base.AncestorOfFold(parsed) {
		limit -= len(base.RDNs)
	}
	var out []coreldap.DirectoryGroup
	for i := 1; i < limit; i++ {
		for _, a := range parsed.RDNs[i].Attributes {
			if strings.EqualFold(a.Type, g.GroupMemberAttribute) && a.Value != "" {
				out = append(out, coreldap.DirectoryGroup{Name: a.Value, DN: rdnString(parsed.RDNs[i:])})
			}
		}
	}
	return out
}

// rdnString writes RDNs back as a DN.
func rdnString(rdns []*goldap.RelativeDN) string {
	parts := make([]string, len(rdns))
	for i, r := range rdns {
		parts[i] = r.String()
	}
	return strings.Join(parts, ",")
}

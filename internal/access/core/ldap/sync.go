package ldap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// GroupStatus is how a directory group stands against the local groups:
// the required_update of the Access search and import.
type GroupStatus string

// The statuses 7.x's RefreshLdapGroupService gives a group a search found.
const (
	// StatusMissing: no local group has the group's name.
	StatusMissing GroupStatus = "DOES_NOT_EXIST"
	// StatusStale: a local group has the name but not the group's DN, which
	// is also how a group created locally, without a DN, stands.
	StatusStale GroupStatus = "REQUIRES_UPDATE"
	// StatusCurrent: a local group has the name and the DN.
	StatusCurrent GroupStatus = "IN_ARTIFACTORY"
)

// Operation is what a refresh does: the operation query parameter.
type Operation string

// The refresh operations the Access API documents.
const (
	OpUpdate          Operation = "UPDATE"
	OpImport          Operation = "IMPORT"
	OpUpdateAndImport Operation = "UPDATE_AND_IMPORT"
)

func (o Operation) imports() bool { return o == OpImport || o == OpUpdateAndImport }
func (o Operation) updates() bool { return o == OpUpdate || o == OpUpdateAndImport }

var (
	// ErrNoConnection refuses a search or refresh whose enabled_ldap names
	// no enabled connection. It matches kernel.ErrInvalid.
	ErrNoConnection = fmt.Errorf("ldap: enabled_ldap names no enabled LDAP connection: %w", kernel.ErrInvalid)
	// ErrChanged refuses to publish a refresh whose connections or group
	// settings changed while the directory was asked: what it read belongs
	// to settings that no longer hold. It matches kernel.ErrInvalid; asking
	// again reads the new ones.
	ErrChanged = fmt.Errorf(
		"ldap: the LDAP settings changed while the directory was asked; nothing was changed: %w", kernel.ErrInvalid)
)

// UnknownUserError refuses a group search or an import-only refresh for a
// user no enabled connection finds, in 7.x's words ("Could not find DN for
// user"). It matches kernel.ErrInvalid.
type UnknownUserError struct{ Name string }

func (e *UnknownUserError) Error() string { return "Could not find DN for user: '" + e.Name + "'" }

// Unwrap makes the refusal match kernel.ErrInvalid.
func (e *UnknownUserError) Unwrap() error { return kernel.ErrInvalid }

// FoundGroup is a directory group with its local status.
type FoundGroup struct {
	DirectoryGroup
	Status GroupStatus
}

// ImportGroup is a group an import names, as a search returned it. Status
// is what the caller saw: DOES_NOT_EXIST creates the group and refuses a
// name taken since, REQUIRES_UPDATE takes over the local group of that name,
// and IN_ARTIFACTORY leaves it alone. An empty Status is DOES_NOT_EXIST,
// the default of 7.x's LdapUserGroup.
type ImportGroup struct {
	Name, DN, Description string
	Status                GroupStatus
}

// Summary is what a refresh did.
type Summary struct {
	// Imported counts the groups created, Updated the local groups whose DN
	// or description was refreshed, and Users the users whose memberships
	// changed.
	Imported, Updated, Users int
}

// String is the refresh's plain-text answer. JFrog documents only that it
// carries the number of groups imported; the words are artifactweave's [I].
func (s Summary) String() string {
	return fmt.Sprintf("Imported %d LDAP groups, updated %d and changed the memberships of %d users",
		s.Imported, s.Updated, s.Users)
}

// Sync searches, imports and refreshes LDAP groups. Every directory call
// runs between two of Tx's transactions, never inside one: the first reads
// the settings, the second rechecks them and publishes, so a change, an
// interruption or a failure in between publishes nothing.
type Sync struct {
	// Settings reads the connections and group settings; its Tx and Guard
	// serve Snapshot, which joins the transaction Tx opens.
	Settings Service
	// Directory is the directory client.
	Directory Directory
	// Users and Groups hold the local identities; their calls join ctx's
	// transaction.
	Users  port.UserStore
	Groups port.GroupStore
	// Tx runs fn in a guarded transaction in which the caller's authority
	// has been checked again, as the Access adapter's ldapTx does.
	Tx func(ctx context.Context, fn func(context.Context) error) error
}

// LocalName is the local name of a directory group: LDAP groups are
// imported in lower case (JFrog's Create a Group page), the one exception
// to group names keeping their case.
func LocalName(name string) string { return strings.ToLower(name) }

// RealmAttributes is the realm_attributes of an imported group, in the form
// JFrog's LDAP page shows for a group created over REST.
func RealmAttributes(name string, strategy accessapi.LDAPGroupStrategy, dn string) string {
	return "ldapGroupName=" + name + ";groupsStrategy=" + string(strategy) + ";groupDn=" + dn
}

// GroupDN is the DN realm attributes name: everything after groupDn=, as
// 7.x's getDnFromRealmAttributes reads it, since a DN may hold ";".
func GroupDN(attributes string) string {
	_, dn, ok := strings.Cut(attributes, "groupDn=")
	if !ok {
		return ""
	}
	return dn
}

// connectionOf returns the enabled connection g reads through.
func connectionOf(snap Snapshot, g accessapi.LDAPGroupSetting) (Connection, error) {
	c, ok := snap.Connection(g.EnabledLDAP)
	if !ok || !c.Enabled {
		return Connection{}, ErrNoConnection
	}
	return c, nil
}

// Search runs g's search, or with username the search of that user's
// groups, and gives each group its status. g need not be stored.
func (s Sync) Search(ctx context.Context, g accessapi.LDAPGroupSetting, username string) ([]FoundGroup, error) {
	if err := ValidateGroup(g); err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := s.Tx(ctx, func(ctx context.Context) (err error) {
		snap, err = s.Settings.Snapshot(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	conn, err := connectionOf(snap, g)
	if err != nil {
		return nil, err
	}
	var found []DirectoryGroup
	if username == "" {
		found, err = s.Directory.Groups(ctx, conn, g)
	} else {
		var entry Entry
		entry, err = s.Directory.Lookup(ctx, conn, username)
		if errors.Is(err, ErrUnknownUser) {
			return nil, &UnknownUserError{Name: username}
		}
		if err == nil {
			found, err = s.Directory.UserGroups(ctx, conn, g, username, entry.DN)
		}
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // the directory's *DirectoryError, which groupError reports as it is
	}
	out := make([]FoundGroup, 0, len(found))
	err = s.Tx(ctx, func(ctx context.Context) error {
		for _, f := range found {
			st, _, err := s.status(ctx, f)
			if err != nil {
				return err
			}
			out = append(out, FoundGroup{DirectoryGroup: f, Status: st})
		}
		return nil
	})
	return out, err
}

// status compares a directory group with the local group of its name.
func (s Sync) status(ctx context.Context, f DirectoryGroup) (GroupStatus, model.Group, error) {
	local, err := s.Groups.Get(ctx, LocalName(f.Name))
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return StatusMissing, model.Group{}, nil
	case err != nil:
		return "", model.Group{}, fmt.Errorf("ldap: read group %s: %w", LocalName(f.Name), err)
	case strings.EqualFold(GroupDN(local.RealmAttributes), f.DN):
		return StatusCurrent, local, nil
	default:
		return StatusStale, local, nil
	}
}

// ImportIn creates or takes over the local groups an import names, all in
// the transaction ctx carries, as Status says; see ImportGroup. A name the
// local rules refuse, or a status that no longer holds, refuses the whole
// import. It reports how many groups it wrote.
func (s Sync) ImportIn(
	ctx context.Context,
	strategy accessapi.LDAPGroupStrategy,
	groups []ImportGroup,
) (int, error) {
	written := 0
	for _, ig := range groups {
		name := LocalName(ig.Name)
		if err := validLocalName(name); err != nil {
			return 0, err
		}
		if ig.DN == "" || !validDN(ig.DN) {
			return 0, refuseGroup("import_groups.group_dn", "must be the group's distinguished name")
		}
		cur, err := s.Groups.Get(ctx, name)
		exists := err == nil
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return 0, fmt.Errorf("ldap: read group %s: %w", name, err)
		}
		attrs := RealmAttributes(ig.Name, strategy, ig.DN)
		switch ig.Status {
		case StatusMissing, "":
			if exists {
				return 0, refuseGroup("import_groups", "names "+name+", which a local group already has; "+
					"search again and import it as REQUIRES_UPDATE to take it over")
			}
			err = s.Groups.Create(ctx, model.Group{
				Name: name, Description: ig.Description, Realm: model.RealmLDAP, RealmAttributes: attrs,
			})
		case StatusStale:
			if !exists {
				return 0, refuseGroup("import_groups", "names "+name+", which no local group has; "+
					"search again and import it as DOES_NOT_EXIST")
			}
			cur.Realm, cur.RealmAttributes, cur.Description = model.RealmLDAP, attrs, ig.Description
			err = s.Groups.Update(ctx, cur)
		case StatusCurrent:
			continue
		default:
			return 0, refuseGroup("import_groups.required_update",
				"must be DOES_NOT_EXIST, REQUIRES_UPDATE or IN_ARTIFACTORY")
		}
		if err != nil {
			return 0, fmt.Errorf("ldap: import group %s: %w", name, err)
		}
		written++
	}
	return written, nil
}

// validLocalName refuses a group name a local group cannot take: the rules
// of 7.x's NameValidator that ImportLdapGroupService applies, of which a
// path separator and control characters matter here [I].
func validLocalName(name string) error {
	switch {
	case name == "":
		return refuseGroup("import_groups.group_name", "is required")
	case strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 }):
		return refuseGroup("import_groups.group_name", "contains illegal characters")
	}
	return nil
}

// resolved is what the directory says of one user for a refresh.
type resolved struct {
	name  string
	found bool
	// groups are the local names of every group the group settings of the
	// connection that found the user give it.
	groups []string
}

// Refresh re-reads the groups of the setting name and publishes them: with
// IMPORT the groups no local group has yet are created; with UPDATE the
// imported groups whose DN or description changed are refreshed, and the
// memberships of every stored ldap-realm user, or of username alone, are
// set to what the directory says now. A user no enabled connection finds
// loses every LDAP membership, as 7.x's security.ldap.group.policy.cleanOnFail
// (on by default) does; one the directory cannot be asked about fails the
// refresh. Nothing is published unless every read succeeded and the
// settings are unchanged at publication.
func (s Sync) Refresh(ctx context.Context, name string, op Operation, username string) (Summary, error) {
	switch op {
	case OpUpdate, OpImport, OpUpdateAndImport:
	default:
		return Summary{}, refuseGroup("operation", "must be UPDATE, IMPORT or UPDATE_AND_IMPORT")
	}
	var (
		snap  Snapshot
		users []string
	)
	if err := s.Tx(ctx, func(ctx context.Context) (err error) {
		if snap, err = s.Settings.Snapshot(ctx); err != nil {
			return err
		}
		if _, ok := snap.GroupSetting(name); !ok {
			return ErrGroupSettingNotFound
		}
		if op.updates() && username == "" {
			users, err = ldapUsers(ctx, s.Users)
		}
		return err
	}); err != nil {
		return Summary{}, err
	}
	g, _ := snap.GroupSetting(name)
	conn, err := connectionOf(snap, g.LDAPGroupSetting)
	if err != nil {
		return Summary{}, err
	}
	if username != "" {
		users = []string{username}
	}
	var found []DirectoryGroup
	var members []resolved
	for _, u := range users {
		r, err := s.resolve(ctx, snap, u)
		if err != nil {
			return Summary{}, err
		}
		members = append(members, r)
	}
	switch {
	case username == "":
		found, err = s.Directory.Groups(ctx, conn, g.LDAPGroupSetting)
	case !members[0].found && !op.updates():
		return Summary{}, &UnknownUserError{Name: username}
	case members[0].found:
		var entry Entry
		if entry, err = s.Directory.Lookup(ctx, conn, username); errors.Is(err, ErrUnknownUser) {
			// Found through another connection: this setting's connection
			// has no groups for the user.
			err = nil
			break
		}
		if err == nil {
			found, err = s.Directory.UserGroups(ctx, conn, g.LDAPGroupSetting, username, entry.DN)
		}
	}
	if err != nil {
		return Summary{}, err //nolint:wrapcheck // the directory's *DirectoryError, which groupError reports as it is
	}
	var sum Summary
	err = s.Tx(ctx, func(ctx context.Context) error {
		now, err := s.Settings.Snapshot(ctx)
		if err != nil {
			return err
		}
		if _, ok := now.GroupSetting(name); !ok {
			return ErrGroupSettingNotFound
		}
		if now.Revision != snap.Revision {
			return ErrChanged
		}
		sum = Summary{}
		if err := s.publishGroups(ctx, op, g, found, &sum); err != nil {
			return err
		}
		for _, r := range members {
			changed, err := PublishMemberships(ctx, s.Users, s.Groups, r.name, r.groups)
			if err != nil {
				return err
			}
			if changed {
				sum.Users++
			}
		}
		return nil
	})
	return sum, err
}

// publishGroups creates the missing groups (IMPORT) and refreshes the ldap
// realm groups whose DN or description changed (UPDATE). A local group of
// another realm keeps its name to itself: a refresh never takes one over,
// only an import that names it as REQUIRES_UPDATE does.
func (s Sync) publishGroups(ctx context.Context, op Operation, g GroupSetting, found []DirectoryGroup, sum *Summary) error {
	seen := map[string]bool{}
	for _, f := range found {
		// Two directory groups whose names differ only in case, or that
		// share a name in two branches, map to one local group; the first
		// the directory returned keeps it, or each refresh would swap them.
		if seen[LocalName(f.Name)] {
			continue
		}
		seen[LocalName(f.Name)] = true
		st, local, err := s.status(ctx, f)
		if err != nil {
			return err
		}
		attrs := RealmAttributes(f.Name, g.Strategy, f.DN)
		switch {
		case st == StatusMissing && op.imports():
			if err := validLocalName(LocalName(f.Name)); err != nil {
				// A directory name no local group can take is skipped, not
				// fatal: the rest of the directory still imports.
				continue
			}
			err = s.Groups.Create(ctx, model.Group{
				Name: LocalName(f.Name), Description: f.Description, Realm: model.RealmLDAP, RealmAttributes: attrs,
			})
			sum.Imported++
		case op.updates() && local.Realm == model.RealmLDAP &&
			(st == StatusStale || local.Description != f.Description):
			local.RealmAttributes, local.Description = attrs, f.Description
			err = s.Groups.Update(ctx, local)
			sum.Updated++
		}
		if err != nil {
			return fmt.Errorf("ldap: publish group %s: %w", LocalName(f.Name), err)
		}
	}
	return nil
}

// resolve asks the enabled connections, in key order, for name, as a login
// would find it, and then every group setting of the connection that found
// it for its groups.
func (s Sync) resolve(ctx context.Context, snap Snapshot, name string) (resolved, error) {
	for _, c := range snap.Connections {
		if !c.Enabled {
			continue
		}
		entry, err := s.Directory.Lookup(ctx, c, name)
		if errors.Is(err, ErrUnknownUser) {
			continue
		}
		if err != nil {
			return resolved{}, fmt.Errorf("ldap: connection %s: %w", c.Key, err)
		}
		groups, err := DirectoryMemberships(ctx, s.Directory, snap, c, name, entry.DN)
		if err != nil {
			return resolved{}, err
		}
		return resolved{name: name, found: true, groups: groups}, nil
	}
	return resolved{name: name}, nil
}

// DirectoryMemberships asks every group setting of c for the groups of
// name, whose entry is dn, and returns their local names, sorted and
// without repeats.
func DirectoryMemberships(
	ctx context.Context,
	d Directory,
	snap Snapshot,
	c Connection,
	name, dn string,
) ([]string, error) {
	var out []string
	for _, g := range snap.GroupsOf(c.Key) {
		found, err := d.UserGroups(ctx, c, g.LDAPGroupSetting, name, dn)
		if err != nil {
			return nil, fmt.Errorf("ldap: group setting %s: %w", g.Name, err)
		}
		for _, f := range found {
			out = append(out, LocalName(f.Name))
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ldapUsers returns the names of the stored users of the ldap realm.
func ldapUsers(ctx context.Context, users port.UserStore) ([]string, error) {
	var out []string
	pages := kernel.NewPages(func(ctx context.Context, p kernel.Page) ([]port.UserSummary, string, error) {
		return users.ListSummaries(ctx, port.UserFilter{}, p)
	}, 500)
	for {
		page, more, err := pages.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("ldap: list users: %w", err)
		}
		for _, u := range page {
			if u.Realm == model.RealmLDAP {
				out = append(out, u.Name)
			}
		}
		if !more {
			return out, nil
		}
	}
}

// PublishMemberships sets user's memberships in ldap-realm groups to the
// groups named, in the transaction ctx carries: it joins those of them that
// exist as ldap-realm groups and leaves every other ldap-realm group. Its
// memberships in groups of other realms are the administrator's and stay as
// they are. A user that is not stored changes nothing. It reports whether a
// membership changed.
func PublishMemberships(
	ctx context.Context,
	users port.UserStore,
	groups port.GroupStore,
	user string,
	names []string,
) (bool, error) {
	u, err := users.Get(ctx, model.FoldUsername(user))
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ldap: read user %s: %w", user, err)
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	changed := false
	for _, name := range u.Groups {
		if want[name] {
			delete(want, name)
			continue
		}
		g, err := groups.Get(ctx, name)
		if err != nil {
			return false, fmt.Errorf("ldap: read group %s: %w", name, err)
		}
		if g.Realm != model.RealmLDAP {
			continue
		}
		if err := groups.RemoveMembers(ctx, name, u.Name); err != nil {
			return false, fmt.Errorf("ldap: leave group %s: %w", name, err)
		}
		changed = true
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		g, err := groups.Get(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("ldap: read group %s: %w", name, err)
		}
		if g.Realm != model.RealmLDAP {
			continue
		}
		if err := groups.AddMembers(ctx, name, u.Name); err != nil {
			return false, fmt.Errorf("ldap: join group %s: %w", name, err)
		}
		changed = true
	}
	return changed, nil
}

// ErrGroupNotInDirectory reports a Create a Group import whose name no group
// of the setting's directory has, which JFrog documents as 404.
var ErrGroupNotInDirectory = errors.New("ldap: no group of the LDAP group setting's directory has that name")

// NamedGroup is the one directory group a Create a Group import names, with
// the local name and realm attributes it is stored under. Publish it with
// PublishNamed, which holds it to the settings it was read through.
type NamedGroup struct {
	DirectoryGroup
	// Local is the local group's name: the directory's, in lower case.
	Local string
	// RealmAttributes are the local group's, as an import from the LDAP
	// Groups screen writes them.
	RealmAttributes string
	setting         string
	revision        string
}

// FindNamed asks the directory of the group setting setting for the group
// whose name attribute is name, as Create a Group's
// import_from_ldap_group_setting does. The name must identify a single
// group: none answers ErrGroupNotInDirectory, more than one a refusal.
func (s Sync) FindNamed(ctx context.Context, setting, name string) (NamedGroup, error) {
	if strings.TrimSpace(name) == "" {
		return NamedGroup{}, refuseGroup("name", "is required")
	}
	var snap Snapshot
	if err := s.Tx(ctx, func(ctx context.Context) (err error) {
		snap, err = s.Settings.Snapshot(ctx)
		return err
	}); err != nil {
		return NamedGroup{}, err
	}
	g, ok := snap.GroupSetting(setting)
	if !ok {
		return NamedGroup{}, ErrGroupSettingNotFound
	}
	conn, err := connectionOf(snap, g.LDAPGroupSetting)
	if err != nil {
		return NamedGroup{}, err
	}
	found, err := s.Directory.GroupsNamed(ctx, conn, g.LDAPGroupSetting, name)
	switch {
	case err != nil:
		return NamedGroup{}, err //nolint:wrapcheck // the directory's *DirectoryError, which groupError reports as it is
	case len(found) == 0:
		return NamedGroup{}, ErrGroupNotInDirectory
	case len(found) > 1:
		return NamedGroup{}, refuseGroup("name", "names more than one group in the directory")
	}
	f := found[0]
	if err := validLocalName(LocalName(f.Name)); err != nil {
		return NamedGroup{}, refuseGroup("name", "is a directory name no local group can take")
	}
	return NamedGroup{
		DirectoryGroup: f, Local: LocalName(f.Name),
		RealmAttributes: RealmAttributes(f.Name, g.Strategy, f.DN),
		setting:         setting, revision: snap.Revision,
	}, nil
}

// PublishNamed runs fn, which stores n, in a guarded transaction only while
// the settings n was read through are unchanged, so a group setting or
// connection deleted or changed while the directory answered stores nothing.
func (s Sync) PublishNamed(ctx context.Context, n NamedGroup, fn func(context.Context) error) error {
	return s.Tx(ctx, func(ctx context.Context) error {
		now, err := s.Settings.Snapshot(ctx)
		if err != nil {
			return err
		}
		if _, ok := now.GroupSetting(n.setting); !ok {
			return ErrGroupSettingNotFound
		}
		if now.Revision != n.revision {
			return ErrChanged
		}
		return fn(ctx)
	})
}

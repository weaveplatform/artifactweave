package ldap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

var (
	// ErrNotFound reports a connection that does not exist.
	ErrNotFound = errors.New("ldap: no LDAP connection has that key")
	// ErrExists reports a create whose key is taken. It matches
	// kernel.ErrInvalid: Access answers it 400, as it does for OIDC providers.
	ErrExists = fmt.Errorf("ldap: an LDAP connection with that key already exists: %w", kernel.ErrInvalid)
)

// secretPassword is the sealed secret's name; the document never holds it.
const secretPassword = "manager_password"

// document is the stored form of a connection: the Access wire form without
// the manager password, plus the revision that directory clients cache by.
// The wire form and its defaults follow artifactory_ldap_setting_v2 and
// 7.x's LdapSetting descriptor (research 05, "LDAP connections").
type document struct {
	Key                      string         `json:"key"`
	Enabled                  bool           `json:"enabled"`
	URL                      string         `json:"ldap_url"`
	UserDNPattern            string         `json:"user_dn_pattern"`
	Search                   documentSearch `json:"search"`
	AutoCreateUser           bool           `json:"auto_create_user"`
	EmailAttribute           string         `json:"email_attribute"`
	PoisoningProtection      bool           `json:"ldap_poisoning_protection"`
	AllowUserToAccessProfile bool           `json:"allow_user_to_access_profile"`
	PagingSupportEnabled     bool           `json:"paging_support_enabled"`
	Revision                 string         `json:"revision"`
}

type documentSearch struct {
	Filter    string `json:"search_filter"`
	Base      string `json:"search_base"`
	SubTree   bool   `json:"search_sub_tree"`
	ManagerDN string `json:"manager_dn"`
}

func toDocument(c accessapi.LDAPConnection, revision string) document {
	return document{
		Key: c.Key, Enabled: c.Enabled, URL: c.URL, UserDNPattern: c.UserDNPattern,
		Search: documentSearch{
			Filter: c.SearchFilter, Base: c.SearchBase, SubTree: c.SearchSubTree, ManagerDN: c.ManagerDN,
		},
		AutoCreateUser: c.AutoCreateUser, EmailAttribute: c.EmailAttribute,
		PoisoningProtection: c.PoisoningProtection, AllowUserToAccessProfile: c.AllowUserToAccessProfile,
		PagingSupportEnabled: c.PagingSupportEnabled, Revision: revision,
	}
}

func (d document) connection() accessapi.LDAPConnection {
	return accessapi.LDAPConnection{
		Key: d.Key, Enabled: d.Enabled, URL: d.URL, UserDNPattern: d.UserDNPattern,
		EmailAttribute: d.EmailAttribute, AutoCreateUser: d.AutoCreateUser,
		PoisoningProtection: d.PoisoningProtection, AllowUserToAccessProfile: d.AllowUserToAccessProfile,
		PagingSupportEnabled: d.PagingSupportEnabled, SearchFilter: d.Search.Filter, SearchBase: d.Search.Base,
		SearchSubTree: d.Search.SubTree, ManagerDN: d.Search.ManagerDN,
	}
}

// Defaults is a new connection before its fields are applied: enabled, users
// created on first login and mapped by mail, poisoning protection, paged
// searches and sub-tree searches on, profile access off. These are 7.x's
// LdapSetting and SearchPattern defaults, with paging from the
// artifactory_ldap_setting_v2 schema, so a connection created with few
// fields reads back as the provider plans it.
func Defaults(key string) accessapi.LDAPConnection {
	return accessapi.LDAPConnection{
		Key: key, Enabled: true, EmailAttribute: "mail", AutoCreateUser: true,
		PoisoningProtection: true, PagingSupportEnabled: true, SearchSubTree: true,
	}
}

// Merge applies a change's set fields to c.
func Merge(c accessapi.LDAPConnection, ch accessapi.LDAPChange) accessapi.LDAPConnection {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	flag := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	flag(&c.Enabled, ch.Enabled)
	set(&c.URL, ch.URL)
	set(&c.UserDNPattern, ch.UserDNPattern)
	set(&c.EmailAttribute, ch.EmailAttribute)
	flag(&c.AutoCreateUser, ch.AutoCreateUser)
	flag(&c.PoisoningProtection, ch.PoisoningProtection)
	flag(&c.AllowUserToAccessProfile, ch.AllowUserToAccessProfile)
	flag(&c.PagingSupportEnabled, ch.PagingSupportEnabled)
	set(&c.SearchFilter, ch.SearchFilter)
	set(&c.SearchBase, ch.SearchBase)
	flag(&c.SearchSubTree, ch.SearchSubTree)
	set(&c.ManagerDN, ch.ManagerDN)
	return c
}

// Service manages the LDAP connections, stored as configuration documents of
// kind configdoc.ConfigLDAP with the manager password as their sealed secret.
// Every method but Snapshot joins the transaction ctx carries; its caller
// holds the administration guard (port.Projects.Guard) across its authority
// check and the write, as the HTTP adapter and the descriptor do.
type Service struct {
	// Configs holds the connections.
	Configs configdoc.ConfigStore
	// Settings holds the internal password switch, which writing an enabled
	// connection turns off while it was never written (put). Nil leaves the
	// switch alone, which only tests that are not about it should do.
	Settings settings.SettingsStore
	// Tx and Guard give Snapshot its own guarded transaction.
	Tx    tx.Runner
	Guard func(context.Context) error
	// Now stamps writes; nil means time.Now.
	Now func() time.Time
	// Random draws revisions; nil means crypto/rand.
	Random io.Reader
}

// stored is a connection as read, with what the caller may not see.
type stored struct {
	conn     accessapi.LDAPConnection
	password string
	revision string
}

func (s Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func decode(d configdoc.ConfigDocument) (stored, error) {
	var doc document
	if err := json.Unmarshal(d.Document, &doc); err != nil {
		return stored{}, fmt.Errorf("ldap: connection %s: %w", d.Key, kernel.ErrStore)
	}
	if doc.Key != d.Key {
		return stored{}, fmt.Errorf("ldap: connection %s names %s: %w", d.Key, doc.Key, kernel.ErrStore)
	}
	return stored{conn: doc.connection(), password: d.Secrets[secretPassword], revision: doc.Revision}, nil
}

func (s Service) load(ctx context.Context, key string) (stored, bool, error) {
	d, err := s.Configs.Get(ctx, configdoc.ConfigLDAP, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return stored{}, false, nil
	}
	if err != nil {
		return stored{}, false, fmt.Errorf("ldap: read connection %s: %w", key, err)
	}
	st, err := decode(d)
	return st, err == nil, err
}

func (s Service) all(ctx context.Context) ([]stored, error) {
	docs, err := s.Configs.List(ctx, configdoc.ConfigLDAP)
	if err != nil {
		return nil, fmt.Errorf("ldap: list connections: %w", err)
	}
	out := make([]stored, 0, len(docs))
	for _, d := range docs {
		st, err := decode(d)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// List returns every connection, by key.
func (s Service) List(ctx context.Context) ([]accessapi.LDAPConnection, error) {
	all, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPConnection, 0, len(all))
	for _, st := range all {
		out = append(out, st.conn)
	}
	return out, nil
}

// Get returns one connection; ErrNotFound when there is none.
func (s Service) Get(ctx context.Context, key string) (accessapi.LDAPConnection, error) {
	st, found, err := s.load(ctx, key)
	switch {
	case err != nil:
		return accessapi.LDAPConnection{}, err
	case !found:
		return accessapi.LDAPConnection{}, ErrNotFound
	}
	return st.conn, nil
}

// Create stores a new connection: Defaults with the change's fields. A key
// in use is ErrExists.
func (s Service) Create(ctx context.Context, ch accessapi.LDAPChange) (accessapi.LDAPConnection, error) {
	if _, found, err := s.load(ctx, ch.Key); err != nil {
		return accessapi.LDAPConnection{}, err
	} else if found {
		return accessapi.LDAPConnection{}, ErrExists
	}
	return s.write(ctx, Defaults(ch.Key), ch, "")
}

// Replace replaces a connection with Defaults and the change's fields, as
// the Access PUT does. A password the change leaves out is kept, so a client
// that never reads it back (it is never returned) can update the rest.
func (s Service) Replace(ctx context.Context, ch accessapi.LDAPChange) (accessapi.LDAPConnection, error) {
	cur, found, err := s.load(ctx, ch.Key)
	switch {
	case err != nil:
		return accessapi.LDAPConnection{}, err
	case !found:
		return accessapi.LDAPConnection{}, ErrNotFound
	}
	return s.write(ctx, Defaults(ch.Key), ch, cur.password)
}

// write merges, validates and stores one connection, always with a new
// revision: an explicit write is a change a directory client must see.
func (s Service) write(
	ctx context.Context,
	base accessapi.LDAPConnection,
	ch accessapi.LDAPChange,
	password string,
) (accessapi.LDAPConnection, error) {
	conn, password, err := prepare(base, ch, password)
	if err != nil {
		return accessapi.LDAPConnection{}, err
	}
	if err := s.put(ctx, conn, password); err != nil {
		return accessapi.LDAPConnection{}, err
	}
	return conn, nil
}

// prepare merges a change into base and its password into the stored one,
// then validates the result.
func prepare(
	base accessapi.LDAPConnection,
	ch accessapi.LDAPChange,
	password string,
) (accessapi.LDAPConnection, string, error) {
	conn := Merge(base, ch)
	if ch.ManagerPassword != nil {
		if *ch.ManagerPassword != "" && conn.ManagerDN == "" {
			return conn, "", refuse(FieldManagerPassword, "needs search.manager_dn")
		}
		password = *ch.ManagerPassword
	}
	if conn.ManagerDN == "" {
		// A kept password belongs to the manager DN the change removed;
		// binding anonymously with it would be meaningless, and keeping it
		// would leave a credential no setting uses.
		password = ""
	}
	if err := validate(conn, password != ""); err != nil {
		return conn, "", err
	}
	return conn, password, nil
}

func (s Service) put(ctx context.Context, conn accessapi.LDAPConnection, password string) error {
	revision, err := s.revision()
	if err != nil {
		return err
	}
	body, err := json.Marshal(toDocument(conn, revision))
	if err != nil {
		return fmt.Errorf("ldap: encode connection %s: %w", conn.Key, err)
	}
	var secrets map[string]string
	if password != "" {
		secrets = map[string]string{secretPassword: password}
	}
	if err := s.Configs.Put(ctx, configdoc.ConfigDocument{
		Kind: configdoc.ConfigLDAP, Key: conn.Key, Enabled: conn.Enabled,
		Document: body, Secrets: secrets, Updated: s.now(),
	}); err != nil {
		return fmt.Errorf("ldap: write connection %s: %w", conn.Key, err)
	}
	if conn.Enabled {
		return s.defaultInternalPasswordsOff(ctx)
	}
	return nil
}

// defaultInternalPasswordsOff writes the internal password switch off when
// an enabled connection is written and the switch was never written: "Enabling
// LDAP disables internal password authentication by default" (JFrog's LDAP
// page; security configuration, "Disable Basic Authentication Method", from
// 7.104.0). An administrator's own choice, on or off, is never overwritten,
// and a later disable or delete of the connection does not turn it back on,
// as the stored Access setting it is in 7.x stays as written [I]. It joins
// the connection's transaction, so the two commit or roll back together.
func (s Service) defaultInternalPasswordsOff(ctx context.Context) error {
	if s.Settings == nil {
		return nil
	}
	var on bool
	err := s.Settings.Get(ctx, settings.SettingBasicAuthEnabled, &on)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, kernel.ErrNotFound):
		return fmt.Errorf("ldap: read the internal password switch: %w", err)
	}
	if err := s.Settings.Put(ctx, settings.SettingBasicAuthEnabled, false); err != nil {
		return fmt.Errorf("ldap: turn internal password login off: %w", err)
	}
	return nil
}

// revision draws a fresh random revision. It is random rather than a counter
// so a connection deleted and created again never repeats one a client has
// cached, and rather than a digest so it reveals nothing of the password.
func (s Service) revision() (string, error) {
	r := s.Random
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("ldap: draw revision: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Delete removes a connection; ErrNotFound when there is none.
func (s Service) Delete(ctx context.Context, key string) error {
	err := s.Configs.Delete(ctx, configdoc.ConfigLDAP, key)
	if errors.Is(err, kernel.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("ldap: delete connection %s: %w", key, err)
	}
	return nil
}

// planned is one descriptor change worked out against the stored state.
type planned struct {
	result   accessapi.LDAPResult
	conn     accessapi.LDAPConnection
	password string
}

// Plan works out every descriptor change against the stored connections,
// field by field: a change to a stored connection keeps what it omits,
// password included, and one that creates a connection starts from
// Defaults. Nothing is written.
func (s Service) Plan(ctx context.Context, changes []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	plans, err := s.plan(ctx, changes)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPResult, len(plans))
	for i, p := range plans {
		out[i] = p.result
	}
	return out, nil
}

func (s Service) plan(ctx context.Context, changes []accessapi.LDAPChange) ([]planned, error) {
	out := make([]planned, 0, len(changes))
	for _, ch := range changes {
		cur, found, err := s.load(ctx, ch.Key)
		if err != nil {
			return nil, err
		}
		p := planned{result: accessapi.LDAPResult{Key: ch.Key}}
		if ch.Delete {
			if found {
				p.result.Outcome = accessapi.LDAPDeleted
			}
			out = append(out, p)
			continue
		}
		base := Defaults(ch.Key)
		if found {
			base = cur.conn
		}
		p.conn, p.password, err = prepare(base, ch, cur.password)
		var refused *invalid
		if errors.As(err, &refused) {
			refused.Key = ch.Key
		}
		if err != nil {
			return nil, err
		}
		switch {
		case !found:
			p.result.Outcome = accessapi.LDAPCreated
		case p.conn != cur.conn || p.password != cur.password:
			p.result.Outcome = accessapi.LDAPUpdated
		}
		out = append(out, p)
	}
	return out, nil
}

// Apply plans the changes as Plan does and writes those that change a
// connection, so applying the same descriptor again writes nothing and keeps
// every revision.
func (s Service) Apply(ctx context.Context, changes []accessapi.LDAPChange) ([]accessapi.LDAPResult, error) {
	plans, err := s.plan(ctx, changes)
	if err != nil {
		return nil, err
	}
	out := make([]accessapi.LDAPResult, len(plans))
	for i, p := range plans {
		out[i] = p.result
		switch p.result.Outcome {
		case accessapi.LDAPDeleted:
			err = s.Delete(ctx, p.result.Key)
		case accessapi.LDAPCreated, accessapi.LDAPUpdated:
			err = s.put(ctx, p.conn, p.password)
		case accessapi.LDAPUnchanged:
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

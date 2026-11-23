package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
)

// Defaults of the zero Client fields: JFrog's LDAP timeouts (the LDAP page,
// "Customize the LDAP Timeout"), artifactory.security.ldap.connect.timeoutMillis
// 10000 and artifactory.security.ldap.socket.timeoutMillis 15000.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultTimeout        = 15 * time.Second
	// DefaultPageSize is the page a paged search asks for when the
	// connection enables paging (RFC 2696). JFrog documents no size [I];
	// 500 stays under Active Directory's default MaxPageSize of 1000.
	DefaultPageSize = 500
)

// Client is the directory client: it implements access/core/ldap's
// Directory over github.com/go-ldap/ldap/v3. Every call opens its own
// connection and closes it before returning, so no connection, bind or
// password outlives the call; the core keys what it remembers by the
// connection's revision.
type Client struct {
	// RootCAs verifies ldaps:// servers; nil uses the system's trust, which
	// on Linux SSL_CERT_FILE and SSL_CERT_DIR extend, as JFrog's LDAPS with
	// a self-signed certificate is set up by trusting its CA.
	RootCAs *x509.CertPool
	// ConnectTimeout bounds the TCP connect and TLS handshake; zero means
	// DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// Timeout bounds each LDAP operation; zero means DefaultTimeout.
	Timeout time.Duration
	// PageSize is the paged search's page; zero means DefaultPageSize.
	PageSize uint32
}

var _ coreldap.Directory = Client{}

// The objectClass values and attributes of a serialized Java object or JNDI
// reference (RFC 2713, RFC 2714): an entry that carries them is what an
// LDAP poisoning attack plants, so poisoning protection refuses it [I].
// JFrog documents the setting only as filtering out "users exposed to
// vulnerabilities"; this client never deserialises an object, so refusing
// the entry is how the protection takes effect here.
var (
	javaClasses = []string{
		"javaObject", "javaContainer", "javaNamingReference", "javaSerializedObject", "javaMarshalledObject",
	}
	javaAttributes = []string{
		"javaClassName", "javaCodebase", "javaSerializedData", "javaFactory", "javaReferenceAddress",
		"javaRemoteLocation", "javaClassNames",
	}
)

func (c Client) connectTimeout() time.Duration {
	if c.ConnectTimeout > 0 {
		return c.ConnectTimeout
	}
	return DefaultConnectTimeout
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c Client) pageSize() uint32 {
	if c.PageSize > 0 {
		return c.PageSize
	}
	return DefaultPageSize
}

// target is where a connection's URL points.
type target struct {
	addr, host, baseDN string
	tls                bool
}

func parseTarget(raw string) (target, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return target{}, &coreldap.DirectoryError{Op: "connect", Reason: "ldap_url is not an ldap:// or ldaps:// URL"}
	}
	t := target{host: u.Hostname(), baseDN: strings.TrimPrefix(u.Path, "/")}
	port := u.Port()
	switch strings.ToLower(u.Scheme) {
	case "ldaps":
		t.tls = true
		if port == "" {
			port = "636"
		}
	case "ldap":
		if port == "" {
			port = "389"
		}
	default:
		return target{}, &coreldap.DirectoryError{Op: "connect", Reason: "ldap_url is not an ldap:// or ldaps:// URL"}
	}
	t.addr = net.JoinHostPort(t.host, port)
	return t, nil
}

// session is one open connection and what ended it.
type session struct {
	conn *goldap.Conn
	ctx  context.Context
	stop func() bool
}

func (s session) close() {
	s.stop()
	_ = s.conn.Close()
}

// dial connects to t within the connect timeout, verifying an ldaps
// server's certificate against RootCAs and its host name. Cancelling ctx
// closes the connection, which ends any operation waiting on it.
func (c Client) dial(ctx context.Context, t target) (session, error) {
	dctx, cancel := context.WithTimeout(ctx, c.connectTimeout())
	defer cancel()
	nc, err := (&net.Dialer{}).DialContext(dctx, "tcp", t.addr)
	if err != nil {
		return session{}, &coreldap.DirectoryError{
			Op: "connect", Reason: "could not connect to " + t.addr + ": " + cause(dctx, err), Err: err,
		}
	}
	if t.tls {
		tc := tls.Client(nc, &tls.Config{ServerName: t.host, RootCAs: c.RootCAs, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(dctx); err != nil {
			_ = nc.Close()
			return session{}, &coreldap.DirectoryError{
				Op: "tls", Reason: "TLS with " + t.addr + " failed: " + cause(dctx, err), Err: err,
			}
		}
		nc = tc
	}
	conn := goldap.NewConn(nc, t.tls)
	conn.SetTimeout(c.timeout())
	conn.Start()
	return session{conn: conn, ctx: ctx, stop: context.AfterFunc(ctx, func() { _ = conn.Close() })}, nil
}

// cause words err, saying so when ctx ended rather than the server.
func cause(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return ctx.Err().Error()
	}
	return err.Error()
}

// failure is a *DirectoryError for an operation that did not complete.
func (s session) failure(op string, err error) error {
	return &coreldap.DirectoryError{Op: op, Reason: cause(s.ctx, err), Err: err}
}

// Authenticate implements Directory. It follows the order of Spring
// Security's BindAuthenticator, on which Artifactory's LDAP login is built:
// the user DN pattern is bound first, and the search runs only when there is
// none or its bind was refused. The search binds as the manager, or stays
// anonymous without one, and must find exactly one entry, which is then
// bound with the password.
func (c Client) Authenticate(
	ctx context.Context,
	conn coreldap.Connection,
	name, password string,
) (coreldap.Entry, error) {
	if password == "" {
		// An empty password would be an unauthenticated bind (RFC 4513
		// section 5.1.2), which succeeds for any DN on many servers.
		return coreldap.Entry{}, coreldap.ErrRefused
	}
	t, err := parseTarget(conn.URL)
	if err != nil {
		return coreldap.Entry{}, err
	}
	s, err := c.dial(ctx, t)
	if err != nil {
		return coreldap.Entry{}, err
	}
	defer s.close()
	if conn.UserDNPattern != "" {
		dn := under(strings.ReplaceAll(conn.UserDNPattern, "{0}", goldap.EscapeDN(name)), t.baseDN)
		err := s.conn.Bind(dn, password)
		switch {
		case err == nil:
			return c.read(s, conn, dn)
		case !goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials):
			return coreldap.Entry{}, s.failure("bind", err)
		case conn.SearchFilter == "":
			return coreldap.Entry{}, coreldap.ErrRefused
		}
	}
	if conn.SearchFilter == "" {
		return coreldap.Entry{}, coreldap.ErrUnknownUser
	}
	if err := c.manager(s, conn); err != nil {
		return coreldap.Entry{}, err
	}
	found, err := c.find(s, conn, t, name)
	if err != nil {
		return coreldap.Entry{}, err
	}
	if err := s.conn.Bind(found.DN, password); err != nil {
		if goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials) {
			return coreldap.Entry{}, coreldap.ErrRefused
		}
		return coreldap.Entry{}, s.failure("bind", err)
	}
	return coreldap.Entry{DN: found.DN, Email: found.GetEqualFoldAttributeValue(conn.EmailAttribute)}, nil
}

// manager binds as the connection's manager DN, or stays anonymous without
// one, as JFrog's search does.
func (c Client) manager(s session, conn coreldap.Connection) error {
	if conn.ManagerDN == "" {
		return nil
	}
	if conn.ManagerPassword.IsZero() {
		return &coreldap.DirectoryError{
			Op: "manager bind", Reason: "search.manager_dn has no search.manager_password to bind with",
		}
	}
	err := s.conn.Bind(conn.ManagerDN, conn.ManagerPassword.Reveal())
	switch {
	case err == nil:
		return nil
	case goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials):
		return &coreldap.DirectoryError{
			Op: "manager bind", Reason: "the directory refused search.manager_dn or its password", Err: err,
		}
	default:
		return s.failure("manager bind", err)
	}
}

// find searches every search base for the user's entry. More than one entry
// leaves the name ambiguous, which Spring's FilterBasedLdapUserSearch
// refuses, and so does this.
func (c Client) find(s session, conn coreldap.Connection, t target, name string) (*goldap.Entry, error) {
	filter := strings.ReplaceAll(conn.SearchFilter, "{0}", goldap.EscapeFilter(name))
	if !strings.HasPrefix(strings.TrimSpace(filter), "(") {
		// JFrog's Entra ID example, sAMAccountName={0}, has no
		// parentheses, which JNDI adds and the protocol needs.
		filter = "(" + filter + ")"
	}
	scope := goldap.ScopeSingleLevel
	if conn.SearchSubTree {
		scope = goldap.ScopeWholeSubtree
	}
	var found []*goldap.Entry
	for _, base := range bases(conn.SearchBase, t.baseDN) {
		entries, err := c.search(s, conn, goldap.NewSearchRequest(
			base, scope, goldap.NeverDerefAliases, 2, int(c.timeout()/time.Second), false,
			filter, attributes(conn), nil,
		))
		switch {
		case goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
			continue
		case goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded):
			return nil, coreldap.ErrUnknownUser
		case goldap.IsErrorWithCode(err, goldap.LDAPResultFilterError):
			return nil, &coreldap.DirectoryError{
				Op: "search", Reason: "the directory refused search.search_filter: " + err.Error(), Err: err,
			}
		case err != nil:
			return nil, s.failure("search", err)
		}
		found = append(found, entries...)
		if len(found) > 1 {
			return nil, coreldap.ErrUnknownUser
		}
	}
	if len(found) == 0 || poisoned(conn, found[0]) {
		return nil, coreldap.ErrUnknownUser
	}
	return found[0], nil
}

// search runs req, through the paged results control when the connection
// enables paging, which some directories need before they answer a search
// whose results exceed their page limit.
func (c Client) search(s session, conn coreldap.Connection, req *goldap.SearchRequest) ([]*goldap.Entry, error) {
	var (
		res *goldap.SearchResult
		err error
	)
	if conn.PagingSupportEnabled {
		res, err = s.conn.SearchWithPaging(req, c.pageSize())
	} else {
		res, err = s.conn.Search(req)
	}
	if res == nil {
		return nil, err //nolint:wrapcheck // classified by the caller
	}
	return res.Entries, err //nolint:wrapcheck // classified by the caller
}

// read reads the entry a DN pattern bound, as the user, for its email. An
// entry the user may not read still logs in, without an email; one that
// poisoning protection refuses does not.
func (c Client) read(s session, conn coreldap.Connection, dn string) (coreldap.Entry, error) {
	entries, err := c.search(s, conn, goldap.NewSearchRequest(
		dn, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 1, int(c.timeout()/time.Second), false,
		"(objectClass=*)", attributes(conn), nil,
	))
	var ldapErr *goldap.Error
	switch {
	case err == nil && len(entries) == 1:
		if poisoned(conn, entries[0]) {
			return coreldap.Entry{}, coreldap.ErrUnknownUser
		}
		return coreldap.Entry{DN: dn, Email: entries[0].GetEqualFoldAttributeValue(conn.EmailAttribute)}, nil
	case err == nil, errors.As(err, &ldapErr) && ldapErr.ResultCode != goldap.ErrorNetwork &&
		s.ctx.Err() == nil:
		return coreldap.Entry{DN: dn}, nil
	default:
		return coreldap.Entry{}, s.failure("read", err)
	}
}

// attributes are those a login reads: the email, and with poisoning
// protection what identifies a planted Java object.
func attributes(conn coreldap.Connection) []string {
	out := []string{"objectClass"}
	if conn.EmailAttribute != "" {
		out = append(out, conn.EmailAttribute)
	}
	if conn.PoisoningProtection {
		out = append(out, javaAttributes...)
	}
	return out
}

func poisoned(conn coreldap.Connection, e *goldap.Entry) bool {
	if !conn.PoisoningProtection {
		return false
	}
	for _, class := range e.GetEqualFoldAttributeValues("objectClass") {
		for _, java := range javaClasses {
			if strings.EqualFold(class, java) {
				return true
			}
		}
	}
	for _, a := range javaAttributes {
		if len(e.GetEqualFoldRawAttributeValues(a)) > 0 {
			return true
		}
	}
	return false
}

// under places a DN relative to the URL's base DN, as JFrog's user DN
// pattern and search bases are.
func under(dn, base string) string {
	switch {
	case base == "":
		return dn
	case dn == "":
		return base
	}
	return dn + "," + base
}

// bases are the search bases, JFrog's pipe-separated list, each under the
// URL's base DN; none means the base DN itself.
func bases(search, base string) []string {
	if strings.TrimSpace(search) == "" {
		return []string{base}
	}
	var out []string
	for b := range strings.SplitSeq(search, "|") {
		out = append(out, under(strings.TrimSpace(b), base))
	}
	return out
}

// Test implements Directory: it connects, binds as the manager when there
// is one, and reads each search base as a login's search would. A
// connection with only a user DN pattern binds as each user and reads
// nothing before that, so connecting is all it can show. JFrog's test body
// has no test user, so these are the steps it can check [I].
func (c Client) Test(ctx context.Context, conn coreldap.Connection) error {
	t, err := parseTarget(conn.URL)
	if err != nil {
		return err
	}
	s, err := c.dial(ctx, t)
	if err != nil {
		return err
	}
	defer s.close()
	if conn.SearchFilter == "" {
		return nil
	}
	if err := c.manager(s, conn); err != nil {
		return err
	}
	for _, base := range bases(conn.SearchBase, t.baseDN) {
		_, err := s.conn.Search(goldap.NewSearchRequest(
			base, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 1, int(c.timeout()/time.Second), false,
			"(objectClass=*)", []string{"1.1"}, nil,
		))
		switch {
		case goldap.IsErrorWithCode(err, goldap.LDAPResultNoSuchObject):
			return &coreldap.DirectoryError{
				Op: "search", Reason: fmt.Sprintf("the search base %q does not exist or cannot be read", base), Err: err,
			}
		case err != nil:
			return s.failure("search", err)
		}
	}
	return nil
}

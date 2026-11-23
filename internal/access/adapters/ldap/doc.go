// Package ldap is the access module's LDAP client: it checks a user's
// password against a directory, tests a connection's settings for the
// Access API, and finds users and reads LDAP groups for group
// synchronisation, over github.com/go-ldap/ldap/v3.
//
// # Role
//
// access module, adapter layer (LDAP). Client implements access/core/ldap's
// Directory; the composition root (internal/app) gives it to core/ldap's
// Logins, which the authenticator asks, and to the access HTTP adapter for
// POST /access/api/v1/ldap/settings/test. No core package imports it
// (decision 0044, dependency rules).
//
// # Inputs
//
//   - A core/ldap Connection: one connection's effective settings and its
//     manager password, read from the revisioned snapshot outside any
//     metadata transaction.
//   - The user name and password a request presented.
//   - Client's options: the CA pool LDAPS certificates must chain to (the
//     system's when nil) and the timeouts and page size, whose zero values
//     are JFrog's defaults.
//
// # Outputs
//
//   - Authenticate: the user's Entry (DN and email), ErrRefused when the
//     directory refused the password, ErrUnknownUser when the search found
//     no single user, or a *DirectoryError naming the step (connect, tls,
//     manager bind, search, bind) and what failed in it.
//   - Test: nil, or the *DirectoryError of the first step that failed. The
//     error never holds a password.
//
// # Invariants
//
//   - A user DN pattern is tried first, then the search, the order of
//     Spring Security's BindAuthenticator on which Artifactory's LDAP login
//     is built. The name is escaped for the DN (RFC 4514) and the filter (RFC
//     4515), so a name cannot widen either; a search that finds more than one
//     entry finds none.
//   - An empty password is refused before any bind: LDAP treats a simple
//     bind without a password as an unauthenticated bind, which succeeds.
//   - Each call opens one connection and closes it; nothing is pooled
//     across connection revisions. A cancelled context closes the
//     connection at once, and every operation runs under the 15 s timeout
//     after a 10 s connect.
//   - With poisoning protection on, an entry carrying a serialized Java
//     object or JNDI reference is no user [I].
//   - Referrals are not followed [I].
//
// # References
//
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "LDAP connections", "LDAP login" and "LDAP groups".
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 298, and "Access and identity", line 557.
//   - Plan:
//     docs/implementation/plan.md#p12-descriptor-export-system-configuration-ldap.
//   - https://github.com/weaveplatform/artifactweave/issues/1833 and
//     https://github.com/weaveplatform/artifactweave/issues/1834
//
// # Status
//
// Implemented P12: password checks and connection tests (LDAP-02, #1833);
// user lookups and group reads by STATIC, DYNAMIC and HIERARCHICAL
// strategy (LDAP-03, #1834), in groups.go.
//
// # Testing
//
//   - directory_test.go runs against a pinned OpenLDAP container
//     (pkg/containertest.OpenLDAP) seeded by accesstest.SeedDirectory:
//     TestAccDirectoryAuthenticate (DN pattern and search logins, wrong,
//     unknown and disabled users, escaping, both bases, paging off),
//     TestAccDirectoryTLS (LDAPS with the container's CA, an untrusted
//     certificate, plain LDAP to the TLS port), TestAccDirectoryTest (the
//     manager bind, a missing base and an unreachable server) and
//     TestAccDirectoryCancellation.
//   - groups_test.go, against the same container with
//     accesstest.SeedDirectoryGroups: TestAccDirectoryGroups (Lookup by DN
//     pattern and search, every group a setting selects, a user's groups by
//     STATIC, DYNAMIC and HIERARCHICAL, a nesting loop, the Entra ID match
//     rule, a refused filter, a missing base and an unreachable server).
//   - publication_test.go and group_publication_test.go run the real
//     directory over the SQLite and PostgreSQL stores:
//     TestAccLDAPLoginPublication (a login whose connection is disabled,
//     changed or deleted while the directory answered stores nothing) and
//     TestAccLDAPGroupPublication (a refresh racing a deleted or changed
//     group setting, or a store failing part-way, publishes no group and no
//     membership; one racing nothing publishes them all and keeps local
//     memberships; a login racing a setting change stands without its
//     groups; no store or error holds the manager password).
//   - fault_test.go runs against an in-process server that answers
//     scripted BER: TestUnitDirectoryFaults (malformed answers, a silent
//     server and TLS handshake, cancellation, referrals, refused filters and
//     failures after each step), TestUnitDirectoryResults (paging, ambiguous
//     and size-limited searches, poisoning protection, unreadable entries),
//     TestUnitDirectoryURLs and TestUnitDirectoryTestFaults.
package ldap

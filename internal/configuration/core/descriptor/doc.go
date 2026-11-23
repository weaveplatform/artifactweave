// Package descriptor reads Artifactory's YAML configuration descriptor and
// applies it (decision 0009): the body of PATCH /api/system/configuration,
// `artifactweave config apply` and the bootstrap descriptor. Named entities
// that do not exist are created, existing ones are updated field by field,
// omitted keys are untouched, and an entry or a string field whose value is
// null is deleted.
//
// # Role
//
// configuration module, core layer: the implementation of
// configuration/api's Descriptor, Export and System. It reaches the artifacts and
// access modules only through their api packages, and the configuration
// documents through platform/configdoc (decision 0044), so applying a
// descriptor is the one transaction that spans modules.
//
// # Inputs
//
//   - Parse: a YAML document and a lookup for its ${NAME} references.
//   - Deps, which Apply and NewAPI take: the unit of work (platform/tx), the
//     artifacts module's Repositories and Layouts, the configuration
//     document port (platform/configdoc) and access's AnonymousAccess,
//     SecurityPolicies and LDAPSettings, all over one store.
//   - DownloadRedirectMinSize: the configuration document port.
//
// # Outputs
//
//   - Parse: a *Patch, the configapi.Plan holding the repository,
//     configuration document and security changes and the count of the
//     document's values. A refusal wraps ErrInvalid, which is
//     configapi.ErrInvalid.
//   - Apply: a configapi.Result naming what was created, updated, deleted
//     and left alone.
//   - DownloadRedirectMinSize: the minimum redirect size that
//     downloadRedirectConfig.fileMinimumSize set, in bytes.
//   - NewAPI: configapi.API, with Descriptor, Export, System and Mail bound over Deps.
//   - Export.XML: one committed, protected XML descriptor of delivered sections;
//     its typed section document is the extension seam for future sections.
//   - The section names (SectionLocal and the rest) and Lookup.
//
// # Invariants
//
//   - One transaction per patch: Apply checks every entry first, then
//     writes them all in one transaction of Deps.Tx, which every port joins
//     through its context. A patch with one bad entry, or one whose last
//     write fails, changes nothing; a port that opened its own transaction
//     would leave half a descriptor applied.
//   - Export owns its transaction and refuses a same-store ambient transaction
//     before reading state, so bytes are returned only after its own commit.
//     Descriptor Apply retains its nested transaction semantics.
//   - Parse before key or store: Parse reads none of Deps, so config apply
//     refuses a broken descriptor before it builds the keyring or opens the
//     store, and that error is reported ahead of a key or database error.
//   - security.ldapSettings entries map their camelCase fields explicitly to
//     access's LDAPChange and are planned and written through access's
//     LDAPSettings, the service behind /access/api/v1/ldap/settings, so the
//     two surfaces share one connection; access's refusals are reported with
//     the descriptor's field names, and the export never carries a manager
//     password.
//   - Parsing is strict: an unset ${NAME}, a section artifactweave does not
//     apply, an unknown field and a null on a field that is not a string are
//     refused, so a pipeline never takes a 200 for settings that changed
//     nothing. Secrets go to the documents' sealed secrets, never their
//     JSON.
//   - Applying a descriptor again changes nothing, and Merged answers the
//     same count of values either time.
//   - Error texts reach clients and logs, so they stay byte-identical.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decision 0029:
//     docs/research/decisions/0029-cdn-delivery-and-download-redirect.md,
//     lines 51 to 55 and 100 to 102 (downloadRedirectConfig.fileMinimumSize,
//     in whole MB, replaces download.redirect.min_size once applied).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (modules reach each other through their api packages).
//   - Research 06: docs/research/06-system-configuration-and-descriptor.md,
//     "What artifactweave serves".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 308 (core/descriptor), line 994 (calls artifacts and access
//     through api/) and lines 1111 to 1116 (descriptor apply as the one
//     sanctioned transaction across modules).
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape,
//     task 7, and #p9d-modular-monolith-migration.
//   - https://github.com/weaveplatform/artifactweave/issues/1077
//
// # Status
//
// Implemented P3: the repository sections, proxies, property sets and
// layouts, with null deletes, for PATCH /api/system/configuration and
// `config apply`. Later phases added the downloadRedirectConfig section
// (decision 0029), the security section's anonymous access switch and the
// bootstrap descriptor. Moved here in P9d behind configuration/api. Planned
// for P12: the remaining YAML sections. CFG-01 adds the administrator-only XML
// export with current authority rechecks and a guarded transaction snapshot.
// PRJ-04 adds the documented singleton mailServer settings for quota delivery,
// with its password separated before persistence and guarded configuration reads.
// LDAP-01 adds security.ldapSettings through access's LDAPSettings, with the
// connections in the XML export's security element. CFG-02 adds urlBase and
// the reverse proxy behind configuration/api.WebServer: its writes build a
// Patch and Apply it under the guard after a current administrator recheck,
// and the export writes both in artifactory.xsd's order. CFG-04 adds
// security.userLockPolicy and security.passwordSettings through access's
// SecurityPolicies, with the REST APIs' rules and both blocks in the export,
// and refuses the other documented security fields with their reasons.
//
// # Testing
//
//   - parse_test.go: the documented keys and spellings, JFrog's example, the
//     count of values, nulls only on strings, the legacy type flags only
//     the repositories API takes, and the refused sections and errors.
//   - apply_test.go: creates, updates, deletes and second applies that
//     change nothing; the dry run; refusals and store errors that write
//     nothing; the deleted repository's trash entry; virtual members,
//     environments and the Terraform reindex; and the transaction, which a
//     failing late write, a failing anonymous access write and a failure
//     after it all roll back whole.
//   - layout_name_test.go: the bodies of the artifactory_repository_layout
//     Terraform resource, whose layouts repeat their own name.
//   - export_test.go and export_consistency_test.go: protected XML, provider
//     names/list and map shapes, optional values, failures, current authority
//     and snapshot consistency against SQLite and PostgreSQL.
//   - redirect_test.go: downloadRedirectConfig and DownloadRedirectMinSize.
//   - mail_test.go: singleton mail settings, secret retention/clearing, no-op
//     enablement, validation, dry-run and enclosing transaction rollback.
//   - ldap_test.go: every ldapSettings field mapped, outcomes through
//     access's binding with a dry run and a second apply, a missing binding,
//     planning and write failures, and the export's element order.
//   - security_test.go: every policy field mapped, the refusals and their
//     reasons, outcomes through access's binding with a dry run and a second
//     apply, locks cleared by turning locking off, refused policies and
//     failed writes that write nothing of their document, the export with
//     the defaults, and the authenticator's cache dropped after a commit.
//   - webserver_test.go: urlBase through Parse, PUT and PATCH with the CDN
//     rule and the cached effective value; reverse proxy writes, reads with
//     current authority, the snippet, refusals that store nothing, rollback,
//     corrupt stored documents and the export's element order.
package descriptor

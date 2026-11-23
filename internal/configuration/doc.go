// Package configuration is the configuration module's constructor: New(Deps)
// binds configuration/api over the descriptor and system cores. Descriptors
// apply configuration across module APIs; P12 Loggers administers process-local
// levels when composition supplies a logging control and owns cleanup.
//
// # Role
//
// configuration module, module root. Only app imports a module root; other
// modules reach configuration through configuration/api (decision 0044,
// dependency rules).
//
// # Inputs
//
// Deps, which app passes New:
//
//   - Store, required: the unit of work a descriptor is applied in and the
//     configuration documents. App passes its composite store, which
//     satisfies it.
//   - Repositories, Layouts and StoragePolicies (the gcConfig and
//     trashcanConfig sections), from the artifacts module's API, and Access,
//     the access module's API, whose AnonymousAccess and LDAP the security
//     section writes. All of them are bound over the same store, so a descriptor's
//     writes commit or roll back together.
//   - Settings, the configuration options from app/config's Configuration
//     (docker.subdomain_match).
//   - Log and Logging, the structured sink and shared process logging control.
//     A logger API is bound only when an actual Logging mechanism is supplied.
//
// # Outputs
//
//   - Module, whose API returns configuration/api's Descriptor (parse and
//     apply a descriptor: PATCH /api/system/configuration, config apply and
//     the bootstrap descriptor), Export (the protected XML GET), and System (the descriptor's settings that
//     other modules read), the narrow Mail.Server reader, and conditional P12
//     Loggers. Settings returns the Settings from Deps. Close restores temporary
//     logger levels and joins timer callbacks.
//   - Store, what the module needs of the metadata store.
//
// # Invariants
//
//   - New does no I/O and calls nothing on the store: the configuration
//     documents are reached on each call, so app can build the module
//     before anything is read. It refuses only Deps without a store.
//   - The bindings return the descriptor core's and the store's errors as
//     they gave them, so every text an apply answers stays byte-identical.
//   - All Loggers operations require a context outside this metadata store's
//     transaction, before entering the logger lock. Composition owns Close:
//     app's runtime calls it after draining; standalone routers require OnClose
//     before binding their own logger service and preserve injected ownership.
//
// # References
//
//   - Context map, configuration row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns system configuration, descriptors, import and export, and backups;
//     phases P12 and P36.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 302-315, and "Composition root", lines 746-804.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders", "Composition and HTTP" and "Names the migration
//     introduces".
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     #p12-descriptor-export-system-configuration-ldap,
//     #p36-migration-backup-and-recovery.
//   - https://github.com/weaveplatform/artifactweave/issues/1094
//
// # Status
//
// Implemented P9d: New, Module and the descriptor/system bindings, with HTTP in
// configuration/adapters/http. Config apply and bootstrap can bind the descriptor
// core over the store before a module exists. P11 adds the narrow Mail.Server
// read port used by notification delivery. P12 CFG-03 implements conditional
// Loggers and owned timer cleanup: an actual Logging control binds the API, and
// the caller owns Module.Close. app's runtime supplies both; standalone router
// composition leaves its logger API unavailable without an OnClose registrar.
//
// # Testing
//
//   - TestUnitNew: a store is required, New calls none of its methods, and
//     the API and Settings are bound.
//   - TestUnitConfigsReachTheStore: the configuration documents the binding
//     reads and writes are the store's, with its errors.
//   - The descriptor's behaviour is tested in configuration/core/descriptor
//     and configuration/adapters/http.
//   - Logger tests cover current authority, transaction refusal, concurrent
//     store/logger lock interleavings and commit failure. Core and app suites
//     prove real levels, expiry, cleanup and generated-client answers.
package configuration

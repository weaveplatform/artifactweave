// Package remote decides how a remote repository read is served: from cache,
// by revalidation, by an upstream fetch or from a negative entry. It holds
// the shared retrieval and cache policy, upstream discovery and folder
// listings, and the vocabulary that the upstream transport, Smart Remote and
// the format slices' upstream rules share with it.
//
// # Role
//
// artifacts module, core layer: the remote retrieval decision, with no HTTP
// client code. The transport (artifacts/adapters/upstream) implements the
// Upstream and Registries ports declared here; Smart Remote
// (artifacts/core/remote/smart) reaches the policy through Interceptor and
// the WithSmart mark; the format slices plug their upstream rules into the
// policy's hooks. None of them is imported back (decision 0044).
//
// # Inputs
//
//   - Driving: the source service (artifacts/core/remote/source), which
//     reads a remote repository through Policy.Read once it has authorised
//     the request; the listing and discovery callers; Smart Remote; and the
//     format slices' upstream rules, which call the policy's exported
//     checks.
//   - Driven: the Upstream and Registries ports, for every upstream
//     exchange; source.Request and source.Permit, the authorised request
//     and its source identity; the store (repositories, the remote cache,
//     the work queue and leases); the item service and its format registry,
//     which publish what a fetch admits; and the policy hooks the
//     application installs (Fetch, GitFetch, PrepareUpload,
//     ProjectResponse, Options, CheckCurrent and Smart).
//
// # Outputs
//
//   - Policy, its Read and the exported checks the slices and Smart call
//     (Check, CacheKey, FlightKey, ReadEntry, Acquire, State, Cooldown,
//     RecordFailure, RecordResponse), with PolicyError, ResponseError,
//     TransportRequest, MetadataContext, MustNotStore and EagerKey.
//   - Discovery, NewDiscovery and DiscoveryScope; the folder listings
//     (FolderListing, MappedFolderListing, ListFolder, ListMappedFolder,
//     ListingEntry, ListingBudget); SmartPeer and ParseSmartPeer.
//   - The ports and hooks: Upstream, Connection, Options and Request;
//     Registries, RegistryClient and ErrRegistryDenied; Outbound,
//     WithOutbound and OutboundFrom; Interceptor, WithSmart and SmartActive.
//   - The shared vocabulary: the remote: sentinels (ErrURL, ErrRequest,
//     ErrRedirect, ErrConfiguration, ErrClosed, ErrConnection, ErrSecurity
//     and ErrBusy), BaseURL, CheckedURL, Origin, Port, RetryAfter, Admission
//     and FlightGroup.
//
// # Invariants
//
//   - Callers authorise the repository and path before consulting either
//     cache or upstream; the policy only rechecks that authority
//     (CheckCurrent) before it publishes.
//   - Core imports neither the transport (artifacts/adapters/upstream) nor
//     Smart Remote; both import it.
//   - Cache aliases and explicit offline mode make no upstream call.
//   - The sentinel texts, cache key terms and remote. log names stay
//     byte-identical.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - docs/implementation/remote-cache-policy.md and
//     docs/implementation/remote-retrieval-contract.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Remote is split fully" and "Names the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 638 to 646 (core/remote).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic RMT
//     (https://github.com/weaveplatform/artifactweave/issues/942).
//   - https://github.com/weaveplatform/artifactweave/issues/1026 and
//     https://github.com/weaveplatform/artifactweave/issues/1027
//
// # Status
//
// Implemented P9. Staged in P9d.
//
// # Testing
//
//   - Most tests are an external test package (remote_test) over a fake
//     upstream (fakeupstream) and a pool from upstreamtest: policy_test.go
//     holds the policy fixture, with policy_fault_test.go,
//     policy_format_test.go, cache_maintenance_test.go,
//     metadata_response_test.go and module_location_test.go; and
//     discovery_test.go, discovery_contract_test.go, listing_test.go and
//     listing_smart_test.go cover discovery and listings. helpers_test.go
//     holds their small helpers, and export_test.go opens the policy's
//     unexported parts to them.
//   - flight_test.go, rules_test.go, listing_parse_test.go,
//     protocol_test.go and smart_peer_test.go are in-package unit tests of
//     the flight group and the admission, the cache rules, the listing
//     parsers, the protocol paths and the Smart peer parser.
package remote

// Package agentpluginshttp serves the agent plugin marketplace files of a
// local Agent Plugins repository over the item kit: at
// /artifactory/api/agentplugins/{repoKey}/{marketplace}, and at the
// repository root through the item routes. Each file is rendered for the
// request, from the plugins the caller may read.
//
// # Role
//
// artifacts module, ecosystem agentplugins, HTTP layer: the Agent Plugins
// slice's protocol handlers, beside the bundle and marketplace rules of
// artifacts/ecosystem/agentplugins. It is named agentpluginshttp so that it
// does not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency rule
// 9).
//
// # Inputs
//
//   - Driving: the router, which lists TryAgentMarketplaceItem first among
//     the item kit's overlays (itemkit.Deps.Overlays), so that an item GET
//     or HEAD of a marketplace file reaches it before the generic lookup,
//     and calls AgentPluginItems' RegisterAgentMarketplaceRoutes right
//     after the item routes.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and the marketplace's files are
//     read from, and which resolves the repository and the caller's access
//     and writes errors; the generic download policy
//     (artifacts/ecosystem/generic); and the format package
//     (artifacts/ecosystem/agentplugins), whose IsMarketplace,
//     IsReservedMarketplace and Marketplace name and render the files.
//
// # Outputs
//
//   - AgentPluginItems: the kit wrapper whose
//     RegisterAgentMarketplaceRoutes mounts GET and HEAD of
//     /artifactory/api/agentplugins/{repoKey}/{marketplace}.
//   - TryAgentMarketplaceItem: the overlay that serves a marketplace file at
//     a local Agent Plugins repository's root, answers 404 for a reserved
//     name that is no marketplace, and leaves every other request to the
//     overlays after it and the generic lookup.
//
// # Invariants
//
//   - A marketplace needs Read on the repository itself, checked before
//     the download policy at the file's own name, so a caller who may not
//     read the repository learns nothing of how it is set (DV-057): then a
//     blacked-out repository, or patterns rejecting the name, answer 404
//     with the item routes' message. It lists each plugin's latest version
//     that the caller may read. Patterns rejecting that version's ZIP leave
//     it listed, as 7.x's stored marketplace lists each plugin's latest
//     version, and refuse only the ZIP's download.
//   - The files are JSON, private and never stored by a shared cache
//     (Cache-Control: private, no-store); HEAD answers the headers alone.
//   - Only a local Agent Plugins repository has marketplace files; any
//     other repository, or a cached one, answers 404 at the API path.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order).
//   - docs/research/33-package-formats.md, "Agent Plugins" (the contract,
//     the marketplace files and the 7.161.32 version boundary).
//   - JFrog Agent Plugins repositories:
//     https://docs.jfrog.com/artifactory/docs/agent-plugins-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule), line 232 (ecosystem/agentplugins)
//     and line 405 (the root's agentplugins.go).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/690,
//     https://github.com/weaveplatform/artifactweave/issues/1086 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats), as the server
// root's agentplugins.go. P9d moved it here (WEB-17).
//
// # Testing
//
// agentplugins_test.go serves the marketplace files over a kittest world
// (fixture_test.go) through a chi router that mounts the item kit's GET
// and HEAD, with TryAgentMarketplaceItem as the only overlay, and
// RegisterAgentMarketplaceRoutes, with fake deciders: what each caller may
// read at both paths, HEAD, the requests left to the generic lookup, the
// refusals, and the failures of the store, the caller and the rendering.
// The route suite test/server/artifacts/agentplugins drives the marketplace
// files through the router, the format's marketplace_test.go covers the
// rendering and its permission filter, and the @agentplugins acceptance
// scenarios (test/acceptance/features/agent_plugins.feature and
// agent_plugins_clients.feature) read them as clients do.
package agentpluginshttp

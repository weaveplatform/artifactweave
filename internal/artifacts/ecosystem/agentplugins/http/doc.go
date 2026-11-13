// Package agentpluginshttp serves the agent plugin marketplace files of a
// local Agent Plugins repository over the item kit: at
// /artifactory/api/agentplugins/{repoKey}/{marketplace}, and at the
// repository root through the item routes. Each is the file the format's
// queued work stores at the repository root, served as any stored file is.
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
//     (itemkit.Store) the overlay is handed and the stored files are read
//     from, whose item service (Items) runs the marketplace work a download
//     owes, and which resolves the repository and the caller's access,
//     serves the file and writes errors; the generic download policy
//     (artifacts/ecosystem/generic); and the format package
//     (artifacts/ecosystem/agentplugins), whose IsMarketplace names the
//     files.
//
// # Outputs
//
//   - AgentPluginItems: the kit wrapper whose
//     RegisterAgentMarketplaceRoutes mounts GET and HEAD of
//     /artifactory/api/agentplugins/{repoKey}/{marketplace}.
//   - TryAgentMarketplaceItem: the overlay that serves a marketplace file at
//     a local Agent Plugins repository's root, and leaves every other
//     request to the overlays after it and the generic lookup.
//
// # Invariants
//
//   - A marketplace file needs Read on its own path at the repository
//     root, checked before the download policy at that path, so a caller
//     who may not read it learns nothing of how the repository is set
//     (DV-057): then a blacked-out repository, or patterns rejecting the
//     name, answer 404 with the item routes' message. Every caller who may
//     read it reads the same stored bytes, with the checksums, dates and
//     size that AQL and the storage API report, which JFrog CLI checks its
//     download against (issue 1292). Patterns rejecting a listed version's
//     ZIP leave it listed, and refuse only the ZIP's download.
//   - A download first runs the marketplace work a completed write left
//     owed, so it never reads a file that lags the write; a file nothing
//     has stored, or a folder at its name, answers 404. HEAD answers the
//     headers alone.
//   - Only a local Agent Plugins repository has marketplace files; any
//     other repository, or a cached one, answers 404 at the API path.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order).
//   - docs/research/33-package-formats.md, "Agent Plugins" (the contract,
//     the marketplace files and the 7.173.8 version boundary).
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
//     https://github.com/weaveplatform/artifactweave/issues/1086,
//     https://github.com/weaveplatform/artifactweave/issues/1090,
//     https://github.com/weaveplatform/artifactweave/issues/1255 and
//     https://github.com/weaveplatform/artifactweave/issues/1292
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats), as the server
// root's agentplugins.go. P9d moved it here (WEB-17). Issue 1255 added
// vscode-marketplace.json, which self-managed 7.173.8 renders. Issue 1292
// serves the stored files in place of a file rendered for each caller.
//
// # Testing
//
// agentplugins_test.go serves the marketplace files over a kittest world
// (fixture_test.go) with the runtime's work queue, through a chi router
// that mounts the item kit's GET and HEAD, with TryAgentMarketplaceItem as
// the only overlay, and RegisterAgentMarketplaceRoutes, with fake deciders:
// the stored file each reader gets at both paths, the file following the
// index, HEAD, the requests left to the generic lookup, the refusals, a
// file not stored, and the failures of the store, the caller and the work.
// The route suite test/server/artifacts/agentplugins drives the marketplace
// files through the router, AQL and the storage API, the format's
// marketplace_test.go and work_test.go cover the files' content and their
// storing, and the @agentplugins acceptance scenarios
// (test/acceptance/features/agent_plugins.feature,
// agent_plugins_clients.feature and agent_plugins_marketplace.feature) read
// them as clients do.
package agentpluginshttp

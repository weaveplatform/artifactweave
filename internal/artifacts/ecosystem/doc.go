// Package ecosystem is the registry of the package format slices: it picks
// the format of a package type, and hands the formats that take server-wide
// settings their own.
//
// # Role
//
// artifacts module, ecosystem layer: the registry of the format slices, one
// slice per package type (decision 0032). Only the composition builds it: the
// artifacts module's New takes Settings from the module's settings, which
// app/config's Artifacts accessor derives from the configuration, and adds the
// keyring; app hands them to the router it builds
// (RouterConfig.FormatSettings), which hands Settings.For to the item, build
// and registry services it builds. The registry is apart from the format
// contract, artifacts/core/format, so that a slice can implement that contract
// without an import cycle. For the same reason core never imports this
// package: every slice it lists imports core, so core takes the registry as a
// format.Registry value (decision 0044, "Artifacts and remote").
//
// # Inputs
//
//   - Settings: the server-wide options of the formats that take any, from
//     the configuration (decision 0009) through app/config's Artifacts
//     accessor, the Formats of the artifacts module's settings:
//     conda.shards_enabled, helm.relative_urls, npm.tag_latest_by_publish and
//     npm.enforce_path, and the four PyPI keys. The artifacts module adds
//     the master key's keyring, with which the Terraform backend's format
//     opens states; app does too for keys rotate, which builds no module.
//   - The slices: each format package's Serves, which says the package types
//     it serves, and its Handler, which implements format.Handler.
//
// # Outputs
//
//   - Settings.For, a package type's format with s applied: the
//     format.Registry of the work queue's item service, which the artifacts
//     module builds, and of the item, build and registry services that the
//     router (app.NewRouter) builds.
//   - For, the same with every format's default settings. The Docker
//     registry and registry upload expiry take it as their format.Registry,
//     since they reach only docker, oci and helmoci repositories, whose
//     format takes no settings; the formattest contract suite picks a
//     fixture's format with it, and tests that write a slice's files set it.
//   - generic.Handler{} for a package type that no slice serves.
//
// # Invariants
//
//   - A package type is generic until its format lands: generic keeps no
//     package index, so a type with no slice is stored but never indexed.
//   - The zero Settings is every format's default, which is 7.x's, so For
//     and Settings{}.For pick the same formats.
//   - A format's settings ride on its handler, so every write that reaches
//     the format through an item service built with Settings.For applies
//     them, whichever route carried it (decision 0034, "Format settings").
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (formats plug in through hooks, and the registry picks one per package
//     type).
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decisions 0034 (npm), 0037 (PyPI), 0039 (Helm) and 0041 (Terraform
//     backend), whose server-wide settings Settings carries.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (the injected registry, no Settings in
//     artifacts/api, and formattest may import the registry) and "Package
//     placement and names" (formats merges into artifacts/ecosystem).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 225 (ecosystem/registry.go, package type to format.Handler), line
//     998 (one file, no longer a package), line 949 (a new format registers
//     here) and line 185 (the artifacts/api settings, which decision 0044
//     overrides).
//   - Research 24: docs/research/24-package-format-handlers.md.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape,
//     #p6-dockeroci, #p7-npm-go-maven-locals,
//     #p8-pypi-nuget-helm-terraform-locals,
//     #p9b-newly-identified-artifactory-formats, the P9c row of #build-status
//     (https://github.com/weaveplatform/artifactweave/issues/753) and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/971
//
// # Status
//
// Implemented P3 to P9c. For picked the generic handler in P3 and Docker's
// image format in P6. P7, format framework v2, made the registry a package
// of its own, added the Go, Maven and npm formats, and added Settings for
// npm's options. P8 added PyPI, NuGet, Helm, Terraform and the Terraform
// backend, with the settings of PyPI, Helm and the Terraform backend; P9b
// and P9c added the formats they implemented, and P9c conda's settings.
// Staged in P9d. Roadmap: the slices move below this directory with their
// ECO tickets, the artifacts settings are built from Settings
// (https://github.com/weaveplatform/artifactweave/issues/1093), and the
// module constructor wires the registry
// (https://github.com/weaveplatform/artifactweave/issues/1094).
//
// # Testing
//
//   - TestUnitGenericKeepsNoIndex: a package type with no slice gets the
//     generic handler, whose hooks change nothing.
//   - TestUnitImagesHaveTheirFormat and TestUnitHelmHasItsFormat: Docker,
//     OCI and Helm OCI share the image format, and classic Helm has its own.
//   - TestUnitNuGetHasItsFormat: NuGet's format, which takes no settings.
//   - TestUnitSettings, TestUnitPyPISettings and
//     TestUnitTerraformBackendSettings: settings reach the format that takes
//     them, and For is the zero Settings.
package ecosystem

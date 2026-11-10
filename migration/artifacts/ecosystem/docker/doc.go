// Package docker is the storage layout of Docker and OCI images in a
// repository's item tree (decision 0031), which the JFrog CLI reads with AQL
// and the storage API: an image is a folder, each tag a folder of its own
// below it and each manifest pushed by digest a folder named sha256:<hex>,
// and every blob a file named sha256__<hex> in the folders of the images
// that use it. Finished uploads wait in the image's _uploads folder until a
// manifest links them.
//
// # Role
//
// artifacts module, ecosystem layer: the storage layout and index hooks of
// the registry slice, one format for the package types docker, oci and
// helmoci (Serves), which share one protocol and one layout (decision
// 0031), and the rules of their remote repositories (remote_*.go). Its
// Handler plugs into the item service through the format hooks (decision
// 0032), so every write and delete in those repositories reaches it
// whichever API carried it: a push, a jf rt upload, a copy, a move or a
// restore. The registry's writes are core's (artifacts/core/registry), and
// its protocol is the slice's http package, ecosystem/docker/http.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for docker, oci and helmoci repositories to
//     Handler; the registry protocol (ecosystem/docker/http), which parses
//     manifests, checks digests, tags and image names, and lists tags,
//     images and catalogs; the registry's writes (artifacts/core/registry),
//     which name upload paths and digest folders, find blobs, manifests and
//     referrers, set a stored manifest's properties and apply Max Unique
//     Tags and Tag Retention (Retained, Manifests); the Docker REST
//     operations (DockerAPI in ecosystem/docker/http), which check a
//     promotion's tag; the registry source service the application builds
//     over the shared policy of registry remotes, whose upstream fetches and
//     cache uploads RegistryContent serves once installed, and whose reads
//     it checks; the registry protocol's remote reads, which name their
//     resources with RegistryResource; and Smart Remote, which the
//     application gives ParseRegistryPath (smart.NewSmart).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored manifests and chart configs and keeps the
//     package index (artifacts/core/pkgindex); the item store's AQL search
//     (store.ItemStore, artifacts/core/aql), through which the lookups find
//     blobs, manifests, tags, images and referrers; and core remote: the
//     shared policy (remote.Policy), through which RegistryContent checks,
//     reads and caches child manifests and configs and opens what the cache
//     holds, and the registry port (remote.Registries), through which it
//     opens upstream registry clients and sends foreign layer requests.
//
// # Outputs
//
//   - The layout: Serves, UploadsFolder, BlobFile, UploadPath, DigestFolder
//     and IsDigestFolder, ManifestFile and ListManifestFile, and the
//     grammar of tags and digests (ValidTag, ReservedTag, ValidDigest and
//     SHA256Hex).
//   - Manifests: Manifest, Descriptor and Platform, ParseManifest with
//     ErrManifestInvalid and MaxManifestSize, the media types
//     (MediaTypeOCIManifest and the rest, MediaTypeHelmConfig), an image's
//     config (Config, ReadConfig), and the properties a stored manifest
//     carries (Properties: ManifestProperty, LabelPropertyPrefix and the
//     rest, and SubjectProperty and ArtifactTypeProperty, from which the
//     referrers API is answered).
//   - Lookups over the item store: FindBlob, FindManifest, FolderManifest,
//     ManifestFolders, Tags, Images, Manifests, Retained, ImageManifests and
//     FindReferrers, with Compare, the specification's order of tags and
//     names, and PushTime, a tag's place among its image's tags.
//   - Handler: the hooks that index images and tags, classify a tag's
//     manifest as metadata, admit writes under the generic deploy policy,
//     settle the index after deletes and reindex a repository; and Chart,
//     the Helm chart a tag holds (decision 0039).
//   - The remote rules: RegistryContent (Install, Read), which serves a
//     remote repository's manifests, blobs, tag lists and referrers
//     verified; RegistryResource, the cache and permission paths of a
//     registry read; and ParseRegistryPath, the registry path grammar.
//
// # Invariants
//
//   - This package never imports internal/server or its own http package
//     (decision 0031). The item service calls its hooks and never imports
//     it; the remote rules use only the item package's cache upload
//     contract and its errors.
//   - An image is a package while it holds any manifest, tagged or not; a
//     tag folder holding one is a version whose files are the folder's
//     items, ordered by the tag's last push; digest folders and _uploads
//     are no versions (decision 0018).
//   - The layout's folder and file names and the manifest properties are
//     what jf searches with AQL and reads through the storage API (research
//     23, 5.1 and 5.2), so they stay byte-identical.
//   - ParseManifest refuses a schema-1 manifest (DV-034), a type other than
//     the four stored, and a descriptor without a valid digest; a manifest
//     larger than MaxManifestSize (4 MiB) is never accepted.
//   - A remote read caches only verified content: every descriptor of a
//     manifest is checked before it is cached, a foreign layer is fetched
//     only under the repository's external dependency patterns and never
//     with registry credentials or custom headers (decision 0007), and a
//     listing joined from pages carries no-store when any page did or
//     carried Vary.
//   - The cache variant registry-v1, the canonical manifest Accept list and
//     the cache paths RegistryResource names stay byte-identical: a change
//     would orphan every cached entry.
//
// # References
//
//   - Decision 0031: docs/research/decisions/0031-docker-oci-registry.md
//     (protocol and storage layout), and research 23
//     (docs/research/23-docker-oci-registry.md).
//   - Decision 0003: docs/research/decisions/0003-repository-model.md
//     (repository model and Docker access method); decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md (format
//     hooks); decision 0018: docs/research/decisions/0018-package-index.md;
//     decision 0039: docs/research/decisions/0039-helm.md (Helm OCI);
//     decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote repositories: proxy and cache).
//   - docs/operations/registry-remote.md (Docker, OCI and Helm OCI remote
//     repositories, and the paths their cache uses) and
//     docs/testing/nimmodel.md (the Docker remote a NIM repository pairs
//     with).
//   - The OCI Distribution Specification v1.1.1:
//     https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md.
//   - JFrog's pages decision 0031 cites:
//     https://docs.jfrog.com/artifactory/docs/docker-repositories,
//     https://docs.jfrog.com/artifactory/docs/additional-docker-information
//     and https://docs.jfrog.com/integrations/reference/promotedockerimage.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 229 (ecosystem/docker, also oci and helmoci), line 382 (one
//     registry protocol for the three types), lines 656 and 657 (the
//     registry remote rules, remote_*.go) and line 1001 (the mapping row).
//   - Plan: docs/implementation/plan.md#p6-dockeroci,
//     #p8-pypi-nuget-helm-terraform-locals, #p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/223 (the
//     storage layout), https://github.com/weaveplatform/artifactweave/issues/227
//     (tags in the package index and tag retention),
//     https://github.com/weaveplatform/artifactweave/issues/624 (remote
//     reads), https://github.com/weaveplatform/artifactweave/issues/1022 and
//     https://github.com/weaveplatform/artifactweave/issues/1023
//
// # Status
//
// Implemented P6, with Helm OCI chart metadata in P8 and remote retrieval
// in P9. Staged in P9d. A reindex of a remote repository still reads the
// repository's own items, not the cache its hooks index (decision 0018), so
// the format contract suite runs Docker without its cache scenario.
//
// # Testing
//
//   - layout_test.go, list_test.go and manifest_test.go: paths, blob,
//     manifest, tag, image and catalog lookups against stored items,
//     manifest parsing and refusals, properties and image configs.
//   - handler_test.go and index_fault_test.go: classification, generic
//     admission, and store failures while indexing; push times.
//   - chart_test.go: a Helm chart's metadata on its tag, and configs or
//     manifests that cannot be read.
//   - referrers_test.go: subjects, artifact types, index children and the
//     referrers lookup.
//   - contract_test.go: the format contract suite
//     (artifacts/core/formattest) for docker, with NoRemoteCache.
//   - remote_content_test.go: registry path validation, listing pagination
//     and its boundaries, descriptor integrity and the foreign layer
//     policy; remote_path_test.go: the parser's name grammar against the
//     transport's.
//   - internal/app's registry remote tests drive the remote rules through
//     the application's wiring, and core remote's smart_registry_test.go
//     gives Smart Remote this package's parser.
//   - The registry protocol's tests (ecosystem/docker/http) and the guard
//     suites test/server/artifacts/docker and test/server/artifacts/helmoci
//     drive the layout through the registry.
package docker

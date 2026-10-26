# VCS remote repositories

Research checked **2026-10-26** against the current JFrog VCS guide, JFrog's
package-class table and VCS API reference, its 2026 GitLab and Git-client support
articles, and `origin/main` at `5666543b`. The current published Artifactory release
baseline is [7.161.32 self-managed and 7.174.0 SaaS](https://docs.jfrog.com/releases/docs/artifactory-release-notes).
This report scopes [P9c AUT-05 #786](https://github.com/weaveplatform/artifactweave/issues/786).

## Documented contract

| Finding | Primary evidence |
|---|---|
| VCS repositories proxy and cache source archives and ref metadata from Git providers. Artifactory supports **remote only** for this package type; local publication, virtual aggregation and federated publication are not applicable. | [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories), [class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients) |
| A VCS repository serves the `api/vcs` REST surface. `git clone`, `fetch`, `pull` and `push` against that repository are **not supported**: it is an archive proxy, not a Git wire server. Use fresh `curl` plus an archive consumer as the documented native-client acceptance path. | [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories), [JFrog Git-client clarification](https://jfrog.com/help/r/artifactory-git-client-commands-git-clone-fetch-pull-push-are-not-supported-against-a-vcs-remote-repository) |
| The main REST operations are `GET /api/vcs/{tags,branches,refs}/{repoKey}/{userOrg}/{repo}`, `GET /api/vcs/download{Tag,Branch,Commit,Release}/{repoKey}/{userOrg}/{repo}/{ref}`, and `GET /api/vcs/download{TagFile,BranchFile}/{repoKey}/{userOrg}/{repo}/{ref}!{filePath}`. Archive formats are `tar.gz` by default or `zip` through `ext`; file routes are documented with ZIP output. | [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories), [tags](https://docs.jfrog.com/artifactory/reference/getvcstags), [branches](https://docs.jfrog.com/artifactory/reference/getvcsbranches), [refs](https://docs.jfrog.com/artifactory/reference/getvcsrefs), [tag](https://docs.jfrog.com/artifactory/reference/downloadvcstag), [branch](https://docs.jfrog.com/artifactory/reference/downloadvcsbranch), [commit](https://docs.jfrog.com/artifactory/reference/downloadvcscommit), [release](https://docs.jfrog.com/artifactory/reference/downloadvcsrelease), [tag file](https://docs.jfrog.com/artifactory/reference/downloadfilewithinvcstag), [branch file](https://docs.jfrog.com/artifactory/reference/downloadfilewithinvcsbranch) |
| JFrog also publishes alternate short routes and `.../{repo}/archive/refs/{branchName|tagName}` subgroup archive routes. The current guide's statement that nested repository paths are unsupported conflicts with its linked API reference and JFrog's newer support article: GitLab subgroup listing and the `/archive/refs/` download form are documented for Artifactory 7.127 and later. Treat the target 7.x subgroup forms as part of this ticket; retain the discrepancy for live parity capture. | [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories), [subgroup branch API](https://docs.jfrog.com/artifactory/reference/downloadVcsBranchFromSubgroupRepository), [subgroup tag API](https://docs.jfrog.com/artifactory/reference/downloadVcsTagFromSubgroupRepository), [JFrog GitLab support article](https://jfrog.com/help/r/artifactory-how-to-proxy-gitlab-in-vcs-repository-and-resolve-packages) |
| GitHub archive endpoints redirect to temporary download URLs; private archive links expire. Artifactory's VCS guide describes remote credentials and optional lenient host authentication for provider CDNs. A VCS adapter therefore needs explicit redirect and credential-origin policy before fetching any URL selected by provider metadata. | [GitHub archive API](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-tar), [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories) |
| The current JFrog Terraform provider's own VCS remote example uses `vcs_git_provider = "GITLAB"`, and its v12.11.4 release notes record GitLab validation. This is stronger target-version evidence than the older provider list in the VCS guide. | [JFrog provider example](https://github.com/jfrog/terraform-provider-artifactory/blob/master/sample.tf), [JFrog provider releases](https://github.com/jfrog/terraform-provider-artifactory/releases/tag/v12.11.4) |

**Response-shape limit.** The public API reference declares JSON for tag/branch
lists and a Git references file for `refs`, but does not publish a usable complete
body example. The repository's generated OpenAPI model currently describes list
entries with optional `name`, `commitId` and `isBranch` and `refs` as a JSON string.
Those exact shapes are **not verified against a live current Artifactory instance**;
[#216](https://github.com/weaveplatform/artifactweave/issues/216) remains the target-version
capture owner. Do not turn the inferred shape into an unqualified parity claim.

**Slash-bearing refs.** [Git permits hierarchical branch and tag names](https://git-scm.com/docs/git-check-ref-format),
but JFrog's [ordinary branch URL](https://docs.jfrog.com/artifactory/reference/downloadVcsBranch)
documents `branchName` as one path parameter without showing how a slash is
encoded. The [subgroup `archive/refs` URL](https://docs.jfrog.com/artifactory/reference/downloadVcsBranchFromSubgroupRepository)
provides an explicit delimiter. We accept literal slash-bearing refs after that
delimiter for GitLab, including one-level owners, and verify them with `curl`.
This path interpretation is an inference; #216 should capture the target
Artifactory behavior. Slash-bearing refs on the ordinary unmarked download
route remain unverified and are not claimed as supported here. A listed ref can
still be retrieved through its resolved commit route where the provider offers
it.

## Existing artifactweave seams

- The [versioned inventory](../implementation/package-format-inventory.json) already
  approves VCS class `R`, but records only the generic item route and a protocol gap.
  `pkg/repo/repo.go` and `pkg/repo/repo_test.go` already enforce remote-only VCS
  configuration. `vcsType`, `vcsGitProvider` and `vcsGitDownloadUrl` are stored but
  not used by a VCS handler; the existing provider enum does not include GitLab,
  despite the current JFrog provider example above.
- `api/src/artifactory/paths-repositories.yaml` and the generated Artifactory server
  interface already declare the standard, alternate and subgroup VCS routes. The
  `artifactoryService` embeds `UnimplementedStrict`, so these operations currently
  answer 501. The implementation should override those methods and delegate to a
  format-owned adapter rather than edit generated code by hand.
- `internal/source/source.go` provides source identity, validated metadata,
  authorized cache paths, bounded upstream transport, redirects, current-policy
  revalidation and cache-only intent. `internal/server/bower/git.go` already has
  bounded Git ref discovery and archive handling for Bower's separate contract;
  sharing small validators is possible, but Bower's package registry and VCS REST
  routes have different public semantics.

## Proposed implementation and verification

1. Build a VCS remote adapter for all documented `api/vcs` operations, including
   the target-version subgroup route family. Use strict path grammar for provider,
   owner/repo, ref, file and `ext`; validate or reject unsupported provider config.
   Add the current GitLab provider value to repository configuration only with a
   cited target-version contract.
2. Resolve tag/branch names to commit identity before archive admission where the
   provider allows it. Store archive bytes under identity and variant-specific
   cache keys; validate archive size, decompression ratio, entry count and paths.
   Preserve upstream bytes and refuse malformed/truncated responses. Refresh
   branch metadata while retaining authorized offline cached results. This is a
   **design inference** from the documented archive and cache behavior, not a
   claim that JFrog uses identical internals.
3. Apply repository read permission before ref disclosure or archive retrieval;
   recheck current permission and source identity before delivery. Pin provider
   URLs to configured host/path, validate each redirect, and never forward caller
   credentials. Test private-provider auth and CDN redirects with explicit allowed
   origins; deny cross-origin credential leaks by default.
4. Add focused format/server tests and a fresh, pinned `curl` acceptance fixture
   that lists refs, downloads unchanged controlled tar/ZIP bytes from a remote
   provider, checks offline cache and refresh, and proves denied/malformed/
   checksum/redirect failures. Include delete/restore/reindex on cached content
   where supported. Update the inventory and the tested connection recipe for
   [UX-01 #791](https://github.com/weaveplatform/artifactweave/issues/791).

The alternate/subgroup operations remain part of the same VCS REST contract. The
issue wording needs to name the documented REST client (`curl`) instead of a
`git clone` test. A separate [provider-adapter ticket #844](https://github.com/weaveplatform/artifactweave/issues/844) is justified for three
legacy VCS configuration values: `STASH`, `OLDSTASH` and `ARTIFACTORY`.
[JFrog lists these choices](https://docs.jfrog.com/artifactory/docs/vcs-repositories)
but does not publish their upstream ref/archive URL mapping. Atlassian documents
the [current Bitbucket Server/Data Center archive endpoint](https://developer.atlassian.com/server/bitbucket/rest/v900/api-group-repository/),
but that alone does not establish Artifactory's pre-5.1 Stash or peer-remote
behavior. Until a target-version capture identifies those mappings, creation of
a VCS repository using those provider values is rejected explicitly. #844
should capture the target URLs, implement the three adapters and their
credential/redirect policy, and verify private/public archive, offline cache,
refresh, integrity and denial cases. This is provider parity still in scope,
not a permanent exclusion from VCS.

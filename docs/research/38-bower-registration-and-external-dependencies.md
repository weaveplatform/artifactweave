# Bower registration, private Git locators, and dependency rewrite

Checked 2026-11-03 for [#768](https://github.com/weaveplatform/artifactweave/issues/768).

## Primary contracts

The [JFrog Bower guide](https://docs.jfrog.com/artifactory/docs/bower-repositories) documents remote/virtual registration, including an SSH locator for a private Stash project while the configured provider URL uses HTTP and a different port. It also documents virtual external-dependency rewriting, an Ant-style URL allowlist, an aggregated remote as cache destination, and the `art://{{owner}}/{{package}}` shorthand. Rewriting changes dependency entries in downloaded `bower.json` files so subsequent installs use the repository cache. These are the remaining LAN-08/LAN-09 paths, beyond the previously delivered local/remote/virtual/federated core.

The [JFrog VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories) describes archive proxies with private credentials in the remote repository's Basic settings. [JFrog's Git-command support note](https://jfrog.com/help/r/artifactory-git-client-commands-git-clone-fetch-pull-push-are-not-supported-against-a-vcs-remote-repository) explicitly distinguishes provider web/REST requests from a Git hosting endpoint. Together with the Bower example, this supports the implementation inference that an SSH registration URL is a locator mapped to the configured provider HTTP service. It does **not** establish generic server-side SSH cloning or an outbound SSH-key API. This implementation therefore keeps server credentials, TLS and destination controls in the existing HTTP source boundary.

## Pinned client implementation

The published [Bower 1.8.14 package](https://registry.npmjs.org/bower/-/bower-1.8.14.tgz), `lib/commands/register.js`, fetches the supplied Git locator before registration. Its bundled `bower-registry-client/lib/register.js` sends a form-encoded `POST /packages` containing `name` and `url`; it expects 201 and explicitly interprets 403 as a duplicate. The native acceptance test therefore uses an actual Git repository, an authenticated SSH upload-pack server and a pinned host key for that client-side check. The application subsequently retrieves HTTP refs and archives independently.

The published [bower-art-resolver 2.0.10 package](https://registry.npmjs.org/bower-art-resolver/-/bower-art-resolver-2.0.10.tgz), `lib/index.js` and `lib/utils.js`, maps an `art://owner/package` source to `refs/owner/package` and `binaries/owner/package.git/target`. It recognizes 44-character hexadecimal refs. Bower's `pluginResolverFactory.js` invokes release selection for semantic ranges and fetches a non-semantic target directly. The documented `$$$art-shorthand-resolver$$$-` prefix is thus an opaque resolver target; the implementation encodes a validated source URL in that target and reauthorizes it on every request. The target is a locator, never an authorization capability.

## Stash provider versions

[Bitbucket Server REST 5.1.0](https://docs.atlassian.com/bitbucket-server/rest/5.1.0/bitbucket-rest.html) defines the STASH archive at `/rest/api/1.0/projects/{project}/repos/{repository}/archive`, with `at`, `format` and optional `prefix`. The corresponding HTTP clone URL is `/scm/{project}/{repository}.git`. Requests use the configured origin and context prefix.

The older endpoint is distinct. Atlassian's published [stash-archive 1.3.2 source JAR](https://packages.atlassian.com/maven-public/com/atlassian/stash/plugin/stash-archive/1.3.2/stash-archive-1.3.2-sources.jar), `ArchiveServlet.java` and `static/archive.js`, use `/plugins/servlet/archive/projects/{project}/repos/{repository}?at=...&format=tar.gz`. The [bitbucket-archive 2.1.5 source JAR](https://packages.atlassian.com/maven-public/com/atlassian/bitbucket/server/bitbucket-archive/2.1.5/bitbucket-archive-2.1.5-sources.jar) retains that servlet as a compatibility adapter. OLDSTASH uses this servlet, without assuming the later REST archive route.

## Implementation boundaries

Registrations are immutable repository-scoped control records, separate from deployable artifacts and disposable remote metadata. Repository deletion cascades them; cache zap does not. SSH locators must use the configured provider hostname and a bounded project/repository pair. The HTTP origin, port, credentials and context come solely from repository configuration.

Virtual rewrites retain raw validated archives in the selected remote cache and rewrite only delivered content. External TAR/GZIP and ZIP archives are bounded by compressed/expanded size, file count and path grammar; links, duplicate files, malformed manifests and integrity failures are refused. Every destination and redirect requires the virtual allowlist, source DNS/IP policy, and origin-scoped credentials. Virtual configuration/incarnation and current caller authority are checked before delivery, including cached/offline reads. Rewritten responses carry their own length and no upstream checksum or validator headers.

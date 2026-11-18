# P10 NPM-05: virtual external dependencies

Checked 2026-11-18 for [#1568](https://github.com/weaveplatform/artifactweave/issues/1568). **[D]** means primary documentation or issue, **[S]** inspected source, and **[I]** an implementation choice pending [#216](https://github.com/weaveplatform/artifactweave/issues/216). No trial or live Artifactory capture was used.

## Primary evidence

- **[D]** The current [JFrog npm guide](https://docs.jfrog.com/artifactory/docs/npm-repositories#automatically-rewrite-external-npm-dependencies) places rewriting, its Ant allowlist and destination remote on the virtual. It lists git shorthand forms and recommends a dedicated cache remote.
- **[D]** [RTFACT-13802](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-13802), fixed in 5.4.0, includes all three dependency fields. Its `gkt` tarball example produces `gkt-gkt-1.0.0.tgz-art-external`.
- **[D]** [RTFACT-18626](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-18626), fixed in 6.17.0/7.0.0, adds the `github:` prefix. Its `eligrey/FileSaver.js#1.3.8` example produces `eligrey-FileSaver.js-1.3.8.tar.gz-art-external`, a registry dist-tag resolving version 1.3.8, with a virtual `/_external/` URL and the same cache filename.
- **[D]/[S]** [RTFACT-20763](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20763), fix version 7.12.5, describes the wrong-bytes filename collision. Its staff comment makes version appending optional and off by default; it does not define a general filename/version parser. Verified official **7.161.26** `ConstantValues.java:723` retains `npmExternalVersioningEnabled` with `FALSE` default.
- **[S]** In the [official source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz), SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`, descriptor `ExternalDependenciesConfig.java:46–58,72–73` initializes disabled/empty patterns/no cache remote. UI `NpmTypeSpecificConfigModel.java:41–43` instead pre-fills `**` via `RepoConfigDefaultValues.java:116–118`. `RepositoryConfigServiceImpl.java:1798–1805` says normal save validation keeps external refs consistent with direct repository refs. The closed npm fetcher/rewriter is absent.
- **[D]/[S]** [npm 11](https://docs.npmjs.com/cli/v11/configuring-npm/package-json#git-urls-as-dependencies) and [npm 12](https://docs.npmjs.com/cli/v12/configuring-npm/package-json#git-urls-as-dependencies) use the default git branch when no ref is present. Their [hosted GitHub archive template](https://github.com/npm/hosted-git-info/blob/v9.0.3/lib/hosts.js#L54-L55) uses codeload and `HEAD`; this is npm evidence, not proof of Artifactory's fetched URL.

## Implementation answers

Rewriting is a request render over the immutable merged member model. Full and abbreviated packuments and version documents rewrite string specs in `dependencies`, `devDependencies` and `optionalDependencies`; unknown manifest fields and the winning `dist.signatures` survive. Tag bindings persist separately as `(virtual, name, spec, source URL, filename, admitted digest)`, survive runtime restart, and disappear when that virtual is deleted. They do not invalidate R8.

**[I]** Tarball names use dependency name plus URL basename. GitHub archive-capable shorthand and git forms use `https://github.com/<owner>/<repo>/archive/<ref>.tar.gz`, with `HEAD` for an omitted ref and `<owner>-<repo>-<ref>.tar.gz` as filename. Unknown git hosts and unsupported refs remain unchanged. Pinned npm 11/12 classify two-segment HTTPS GitHub URLs as hosted repositories, including names containing `.git`; longer release/archive URLs stay ordinary tarball URLs. Vendor recognition beyond the documented forms remains [I]. The allowlist checks the actual initial archive URL and every redirect, forbids an HTTPS downgrade, and treats configured `[]` as matching nothing. Cache selection requires a direct remote member of the same type and refuses `allowAnyHostAuth`, as Bower does. These runtime details remain inferred beyond the primary examples.

**[I]** Scoped tarball naming removes the leading `@` and replaces the scope slash. Tag-unsafe filename bytes are encoded injectively: pinned npm 11/12 reject raw `@` and `+` in dist-tags. Established specs remain stable; a new URL colliding with one gets a deterministic URL-hash suffix. The virtual archive URL identifies the persisted package/spec, so a later render cannot redirect a previous selection to another source. With versioning off, a different URL at one cache filename is fetched through normal source policy again; an offline/cache-only read additionally checks the binding's admitted digest. It never substitutes another URL's bytes.

**[I]** With `npm.external_dependencies_versioning` on, the last numeric version
hint in the URL directories is appended unless already present in the filename.
The historical 20.5.0.0 example is a URL hint, not a valid npm SemVer assumption.
If neither URL path nor basename provides a version, the filename stays unchanged,
even when the validated archive declares a version. This explicit fallback avoids
an unsourced second publication/alias path; collision checks remain active in
both modes. A server control pins a versionless URL with an actual 2.0.0 archive.

**[I]** Real member versions and existing member tags win collisions. Distinct external archives declaring one version with different bytes answer 502 instead of aliasing. Missing/wrong-name/malformed archives answer 502; actual mapping-store and server-owned copy faults remain 500. Real remote permission, cold-fill Deploy/Cache, blackout/pattern, offline/no-store, origin restriction, credentials and late source/caller checks remain in force. No external record authorizes a read.

## Other-kind disposition

The extraction preserves Bower's existing URL, naming, git and response behavior, including its pre-existing external guard fault answers. [#1689](https://github.com/weaveplatform/artifactweave/issues/1689) separately records actual authority-statement failures mapped to 503 and served-copy read failures mapped to 502 on exact main; it distinguishes closed #1269/#1462/#1488 and remains outside this npm implementation. The source trusted-policy-path defect is [#1670](https://github.com/weaveplatform/artifactweave/issues/1670); #1587's file-backed cold-fill Cache permission is intended.

# Remote cache publication policy paths

Checked 2026-11-18 for [#1670](https://github.com/weaveplatform/artifactweave/issues/1670). [D] is primary documentation, [S] inspected source and [I] an internal implementation choice or unsettled compatibility point. #216 tracks the broader 7.x inference audit. No trial or live Artifactory capture was used.

## Primary contract

[D] Current [remote repository documentation](https://docs.jfrog.com/artifactory/docs/remote-repositories) describes on-demand cache publication and repository include/exclude filters applying to every user. The [PyPI guide](https://docs.jfrog.com/artifactory/docs/pypi-repositories) supports direct and virtual resolution through the proxy. Neither specifies this implementation's internal admission value.

[D] The public [RTFACT-17256 record](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-17256) quotes JFrog support explaining why project-name patterns do not block PyPI files stored under hash paths, and discusses excluding the `.pypi/<project>.*` metadata instead. This supports the existing distinction between a canonical protocol URL and the upstream storage/policy namespace; it does not establish a new authorization exemption.

[S] Verified official OSS 7.161.26 source (SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`): `RealRepoBase.assertValidPath` (lines 126–140) checks blackout and patterns. `RemoteRepoBase.downloadAndSave` (lines 824–855) checks cache-deploy rights using the remote resource's repository path before saving it. Closed PyPI adapter details and a canonical/policy split inside our cache are not established by these sources [I].

## Reproduction and choice

Exact main `088fca937275992da5ae0a3cdf680e43dffd7262`, real SQLite and PostgreSQL 18.6, repeated twice: a valid advertised `files/demo-1.0-py3-none-any.whl` under `includesPattern=files/**` downloads once but cold direct publication answers 502 and cold virtual publication 404. Both no-store and previously cached controls answer 200 with identical bytes. Instrumentation observes source policy acceptance followed by generic admission refusing the canonical `demo/1.0/<wheel>` path. Allowing that extra canonical namespace alone makes publication succeed.

[I] An immutable `format.CachePolicy` binds the source incarnation, repository, canonical target and already-authorized policy path. Only source authorization of an artifact with a full pattern check mints it, into private request-scoped Permit state. Remote upload transports it; item cache admission checks the binding against its token and requires the existing current-authority fence. Private Deploy state carries it to a format's pattern-path accessor. Archive inspection, checksums, canonical storage identity, permissions and publication transactions retain their existing paths and checks. Ordinary deployments, copies and moves cannot populate that private state. The value changes a pattern namespace; it grants no permission.

## Consumer audit

| Existing PolicyPath consumer | Admission disposition |
| --- | --- |
| PyPI distribution files | Artifact with full policy check: bind the advertised upstream path, retaining canonical item/cache identity. |
| Conan internal manifest | Artifact with `BlackoutOnly=true`: no binding, since no pattern namespace was authorized. Preserve the canonical admission baseline. |
| PyPI index/sidecar, npm, Cargo, CocoaPods, Conda, CRAN, Helm index, Ansible, Bower, Hex, LuaRocks, Pub, Puppet and Bazel metadata; VCS refs/version queries | Metadata or versions: no binding. Raw metadata and its existing policy/admission rules remain unchanged. |
| Docker and NIM published metadata | Existing `PublishMetadata` opt-ins have no PolicyPath binding; keep their current item/index layouts and admission. |
| Formats with their own pattern admission (Maven, npm, NuGet, agent/skill and Hugging Face formats) | No current full-pattern artifact PolicyPath consumer bypasses generic admission. Their canonical checks remain unchanged. |

An initially broader artifact-only minting rule changed Conan's blackout-only manifest lookup. Exact-main six-row controls showed why this was invalid: file-only includes give list/file 502/502; manifest-only includes give 200/404; unrestricted includes give 200/200. Requiring full pattern authorization restores all six answers. The baseline cold helper refusals are scoped controls, not a newly established vendor defect.

Validation-time changes to patterns, caller Read permission or source revision still stop publication. `Begin` deliberately leaves an empty CAS tombstone after a started fill; refusal must leave no item, body or negative entry. A source-path exclusion before a fill leaves no row at all. #1587's file-backed cold-fill Deploy/Cache requirement is retained.

## Separate finding during qualification

The new Conan matrix originally reused two subtest labels, which added `#01` to one temporary directory. Exact-main driver and application probes showed a separate SQLite file-URI bug: `state#one` and `state#two` share database state, including both PyPI and Conan repositories. Explicit distinct fixture labels restore isolation here. [#1693](https://github.com/weaveplatform/artifactweave/issues/1693) owns the shared SQL repair; ordinary paths remain independent.

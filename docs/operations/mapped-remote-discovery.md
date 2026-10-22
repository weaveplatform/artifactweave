# Mapped remote discovery

P9 [#672](https://github.com/weaveplatform/artifactweave/issues/672) supplies the
permission and cache contract used by upstream version/GAVC search in
[#213](https://github.com/weaveplatform/artifactweave/issues/213). A remote can use
different `repoLayoutRef` and `remoteRepoLayoutRef` definitions. Discovery reads
upstream metadata under its original URL, then projects artifact paths into the
canonical repository layout before checking permission or returning a download URL.
It does not fetch artifact bodies or create synthetic item records.

Canonical query scopes come from complete layout folders containing the known
coordinates. A caller granted `org.acme/lib/**` can discover that Gradle-layout module
from a Maven upstream under `org/acme/lib/`; an upstream lexical path grant supplies
no canonical authority. Every returned file is checked again against current
canonical Read permission, repository patterns, principal/token state, source and
credential revisions immediately before disclosure.

Some layouts put the module or version only in the filename. A query with no
representable canonical module folder requires actual repository-root Read; a grant
on an unrelated descendant cannot initiate the broader scan. This trusted root mode
is available only to validated metadata GETs, never from a client-supplied flag.
Version-only results need a real version-folder grant or evidence from permitted
actual files; a guessed POM path is never used as authority.

## Lossless paths and descriptor authority

Artifact projection retains every coordinate, including classifier, type and
integration revision. The canonical download path must have exactly one upstream
candidate whose lossless forward projection returns that same canonical path. The
remote lookup uses this paired rule; ordinary copy/move translation is unchanged.

For example, Maven `org/acme/lib/1.0/lib-1.0.pom` projects to Gradle
`org.acme/lib/ivy-1.0.xml`. That canonical descriptor downloads the exact original
POM bytes and preserves upstream POM validation. The alternate spelling
`org.acme/lib/1.0/lib-1.0.pom` is refused before outbound I/O, because it is not the
canonical projection of that upstream POM. A normal mixed JAR/POM folder therefore
works without creating two local identities for the same upstream representation.

Custom layouts and Ivy layouts work when the round trip retains their coordinates.
A specific path that drops an Ivy type, a classifier, or an incompatible integration
revision fails explicitly. Discovery rejects that unsupported representation before
durable or transient admission instead of returning an upstream path or silently
omitting an ambiguous requested artifact. This is a per-path constraint, not a
blanket exclusion of differing layouts.

## Cache and mutation fences

Raw discovery documents use private canonical cache paths, separate from their
upstream URL, with both layout definitions in the representation variant. Upstream
folder/control-document cursors stay internal. HTML and Smart Folder Info reuse the
same bounded parser and source policy: 100 pages and 10,000 entries across an entire
operation, with ordinary freshness, offline, cache-only and no-store behavior.

Creating, changing or deleting a referenced layout document advances the affected
remote source generation inside the same configuration-store transaction. An
in-flight response using the old mapping cannot publish or be delivered after that
edit. Rollback restores both the layout and source state; the fence persists across
database restart. Local repositories and unrelated remote layouts are unaffected.

The query-scope/root rule, paired descriptor canonicalization and exact error
behavior are implementation inferences **[I]**. Capture their live Artifactory
comparison under RC-8/[#216](https://github.com/weaveplatform/artifactweave/issues/216);
the implementation and its tests do not claim a vendor trace.

Coverage includes mixed Maven/Gradle listings and descriptor downloads, custom/Ivy
type/classifier/revision mapping, Smart metadata, canonical-only grants, current
root revocation before publication/delivery, lossy admission in durable and no-store
modes, layout edits during fetch, and memory/SQLite/PostgreSQL store contracts.
Native search qualification is recorded by the consuming #213 change.

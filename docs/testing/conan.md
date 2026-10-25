# Conan native repository guide

Conan repositories use `/artifactory/api/conan/<repo-key>`. Conan 2 is the
current client; Conan 1.66 is retained for legacy projects with revisions
enabled. The adapter serves local, remote, virtual and one-node federated
repositories. A virtual repository reads its members in configured order and
publishes to its configured local default deployment repository.

## Connect a client

Create a local repository with `packageType: conan`, then configure a remote:

```sh
conan remote add aw https://registry.example/artifactory/api/conan/conan-local
conan remote login aw <username>
conan create .
conan upload 'my-package/1.0' -r aw --confirm
conan download 'my-package/1.0' -r aw
```

For Conan 1, set `CONAN_REVISIONS_ENABLED=1`, authenticate with
`conan user <username> -r aw`, and use Conan 1's `conan create`, `upload --all`
and `download` syntax. Use an access token or password at the login prompt.

The native API supports recipe and binary package revisions, file listings,
latest revision selection, search, client upload and download, and revision
deletion. `conanmanifest.txt` is the commit marker. Before committing a
revision, the server checks the manifest's MD5 entries against the uploaded
files or the safely unpacked archive. Incomplete revisions do not appear in
search or downloads. Reindex (`POST /artifactory/api/conan/<repo-key>/reindex`)
rebuilds the package index from committed manifests; deletion and restore use
the existing item lifecycle and rebuild it as needed.

Remote repositories use the shared source service for upstream access and
cache policy. Metadata is bounded and validated before disclosure; downloadable
files are pinned to the manifest digest, and archives are checked against the
manifest contents. Repository and member permission checks apply before names
or bytes are returned. A virtual upload requires a local default deployment
target and write permission on both the virtual repository and that target.

## Qualification

`TestAccConanClients` in `internal/server/conan_client_test.go` uses pinned
Conan 2.33.0 and Conan 1.66.0 to create, upload and download real recipe and
binary package revisions through local, one-node federated and virtual
repositories after clearing each client cache. The Linux acceptance job
installs both exact versions and runs those tests. `TestAccConanRemoteClient`
in `internal/app/conan_client_test.go` also downloads each client's published
recipe and binary package through a remote cache backed by the production
source policy. Local and remote/virtual
protocol, uncommitted revision and generic-upload boundary tests are in
`internal/server/conan_test.go`. The production source/cache test in
`internal/app/conan_remote_test.go` covers remote fetch, cache-only reads,
offline reads, integrity failures, cache exclusion and same-origin redirects.

The protocol and class map follow the [JFrog Conan repository guide](https://docs.jfrog.com/artifactory/docs/conan-repositories)
and [repository client support matrix](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients).
Both current Artifactory 7.x pages were checked on 2026-10-25. The client
versions tested here are Conan 2.33.0 and legacy Conan 1.66.0.

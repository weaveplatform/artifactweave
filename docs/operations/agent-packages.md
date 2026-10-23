# Agent Packages repositories

Agent Packages repositories serve Microsoft Agent Package Manager (APM) 0.23.0
and later. Create a `local` or `federated` repository with
`packageType: agentpackages`. A federated repository serves its own content on
this node; peer convergence belongs to P13. Remote, virtual, Smart Remote and
ordinary replication are outside the documented Agent Packages class matrix.
The [P9b qualification ledger](../testing/phase9b-signoff.md) links the
native local and one-node federated publish/install scenarios and their gate.

## Connect and publish

The native registry base is
`https://<host>/artifactory/api/agentpackages/<repo>`. In an APM project with
an `apm.yml`, configure the client and a bearer token:

```sh
apm experimental enable registries
apm config set registry.<repo>.url https://<host>/artifactory/api/agentpackages/<repo>
apm config set registry.<repo>.token <token>
apm config set registry.<repo>.default true
apm publish --package <owner>/<name> --registry <repo>
apm install '<owner>/<name>#^1.0.0' --target claude
```

Keep the token outside shared project files. The client also accepts an
`APM_REGISTRY_TOKEN_<REPO>` environment variable, with hyphens in the
repository name converted to underscores; the native acceptance test uses
that form. `jf setup apm` and `jf agent apm` are optional wrappers that need
both the JFrog CLI release with APM integration and a native APM binary.

APM publishes one ZIP at
`<owner>/<name>/<name>-<version>.zip`. The archive carries `apm.yml` at its root,
or an `apm.lock.yaml` fallback for manually supplied archives. A descriptor's
name and version must agree with the path. Invalid ZIPs, traversal entries,
symlinks, duplicate entries and archives beyond the bounded parser limits are
rejected. Published versions are immutable through both APM and ordinary item
PUTs. An operator can still delete and restore an archive through Artifactory
item APIs; the package index follows those operations.

## API and permissions

Under the registry base, APM v1 supports:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `v1/packages/<owner>/<name>/versions` | Read visible versions and SHA-256 digests |
| `GET` | `v1/packages/<owner>/<name>/versions/<version>/download` | Download exact ZIP bytes |
| `PUT` | `v1/packages/<owner>/<name>/versions/<version>` | Publish `application/zip` |
| `GET` | `v1/search` | Search visible packages with `q`, `type`, `tag`, `limit`, `offset` |

Version listing and search include only archives the caller may read. Publish
requires Deploy; reads use artifact Read; a denied caller cannot list or fetch
the package. Downloads pass through the ordinary item path's download policy.
The format's reindex handler rebuilds metadata from stored ZIPs. The search
response shape and detailed error statuses await
target-instance comparison in [#216](https://github.com/weaveplatform/artifactweave/issues/216).

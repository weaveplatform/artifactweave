# Cargo repositories

Cargo 1.90.0 is the pinned client in `test/acceptance/clients/cargo.Dockerfile`.
The [JFrog Cargo guide](https://docs.jfrog.com/artifactory/docs/cargo-repositories),
[JFrog class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients),
and [Cargo sparse index](https://doc.rust-lang.org/cargo/reference/registry-index.html)
and [web API](https://doc.rust-lang.org/cargo/reference/registry-web-api.html)
specifications were checked on 2026-10-24. The approved classes are local,
remote, and federated. Cargo virtual repositories are outside the signed
inventory.

## Connect a client

Create a `cargo` repository and add this to `.cargo/config.toml` or
`$CARGO_HOME/config.toml`:

```toml
[registry]
global-credential-providers = ["cargo:token"]

[registries.weave]
index = "sparse+https://REGISTRY/artifactory/api/cargo/REPOSITORY/index/"
```

Provide an Artifactory access token as `CARGO_REGISTRIES_WEAVE_TOKEN`. Cargo
sends its configured token in the `Authorization` header. For a password based
test setup, the token value may be `Basic ` followed by base64 of
`username:password`; use an access token in normal deployments. The sparse
`config.json` advertises this repository's native API and crate download URL.

```sh
cargo publish --registry weave
cargo build
cargo yank --registry weave --version 1.0.0 hello_cargo
```

The source manifest of a consumer uses
`hello_cargo = { version = "1", registry = "weave" }`. The repository also
accepts a direct Artifactory item PUT at
`/artifactory/REPOSITORY/crates/NAME/NAME-VERSION.crate`; it validates the
archive's `Cargo.toml` identity and publishes its basic index record. Cargo
native publish carries dependency and feature metadata for full resolution.

## Repository behavior

Local and one-node federated repositories validate crate identity, store
immutable versions, serve sparse index records and archive bytes, and support
yank and unyank. Delete and trash restore change the live sparse view, which
is derived from the current item tree. The Cargo reindex operation validates
stored crates and their metadata. A remote repository expects its `url` to
name an HTTP sparse index root; the remote adapter reads `config.json`, index
rows and checksum-pinned crates through the shared source policy. The source
policy controls refresh, offline cache, redirects, credentials, and
destination checks. A legacy Git index URL is not an HTTP sparse index root.

JFrog's guide describes a federation URL workaround through Cargo source
replacement, while the signed class inventory approves federated Cargo.
The acceptance run proves publication and resolution on **one federated
node**. Cross-peer convergence and registry URL replacement remain the P13
scope; this implementation does not claim those behaviors.

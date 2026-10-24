# Connect Mix to a Hex repository

Hex repositories use the signed registry v2 protocol at
`/artifactory/api/hex/{repo-key}`. Local, remote and virtual repositories are
supported. The federated class is not offered for Hex by the current JFrog
class table or the repository configuration API.

Create an RSA key pair in Artifactory and assign it as `primaryKeyPairRef` on
each local or remote repository. A virtual repository can use its `keyPair`
field. A remote repository also needs the upstream registry's `hexPublicKey`;
the source must be the actual upstream signing key, not the Artifactory key.
The server verifies upstream registry signatures and package checksums, then
signs the filtered view for its own Mix clients. Missing or invalid keys fail
closed.

When a remote package's dependency names the same upstream repository, the
signed downstream metadata names the Artifactory repository instead. This
keeps transitive resolution through the authorized proxy. Dependencies that
explicitly name a different registry retain that destination.
Upstream retirement, advisory and publication fields are retained for readable
releases; advisory entries belonging only to hidden releases are excluded.

Download the public key for the repository and register it with Mix:

```sh
curl --fail --location --header "Authorization: Bearer $ARTIFACTORY_TOKEN" \
  "$ARTIFACTORY_URL/artifactory/api/hex/$HEX_REPO/public_key" \
  --output publickey.pem
mix hex.repo add "$HEX_REPO" \
  "$ARTIFACTORY_URL/artifactory/api/hex/$HEX_REPO" \
  --auth-key "Bearer $ARTIFACTORY_TOKEN" --public-key ./publickey.pem
```

Use the same repository name in `mix.exs`:

```elixir
defp deps do
  [{:decimal, "2.0.0", repo: "hex-local"}]
end
```

Then run `mix deps.get` and `mix deps.compile`. To deploy to a local repository,
upload a Hex package archive with its original bytes and canonical path:

```sh
curl --fail --location --request PUT \
  --header "Authorization: Bearer $ARTIFACTORY_TOKEN" \
  --upload-file decimal-2.0.0.tar \
  "$ARTIFACTORY_URL/artifactory/hex-local/tarballs/decimal-2.0.0.tar"
```

The server checks the outer archive, inner checksum, `metadata.config`, archive
paths, package name and version. `names`, `versions` and `packages/{name}` are
generated from readable releases and signed; they cannot be deployed as files.
Delete, restore and reindex use the standard item lifecycle. Remote content
passes through the shared source/cache policy and remains resolvable from a
verified cache in offline mode.

The acceptance suite uses Elixir 1.16.3, OTP 26.2.5.21 and Hex 2.6.0. It
checks local and virtual `mix deps.get`, a remote redirect, a fresh Mix
client resolving from the server cache after upstream shutdown, and Ecto's
transitive telemetry and decimal requirements through the remote proxy. JFrog's
[Hex guide](https://docs.jfrog.com/artifactory/docs/hex-repositories) and the
[Hex self-hosting guide](https://hex.pm/docs/self-hosting) were checked on
2026-10-25. JFrog documents a Hex 2.4.0 OAuth issue for public remote
registries; its workaround is `mix hex.repo set <repo> --no-oauth-exchange`.

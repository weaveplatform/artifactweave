# npm registry signatures

An npm local or federated repository whose `primaryKeyPairRef` names an ECDSA key
pair signs each version's `dist.signatures` and lists its keys at
`/artifactory/api/npm/<repo>/-/npm/v1/keys`, as 7.x does from 7.83.1
([decision 0034, "Signatures"](../research/decisions/0034-npm.md#signatures), #362).
The [native scenario](../../test/acceptance/features/npm_signatures.feature) runs
`npm audit signatures` with both pinned npm clients, npm 11 on Node 26
([node.Dockerfile](../../test/acceptance/clients/node.Dockerfile)) and npm 12.2.1 on
Node 24 ([node-npm12](../../test/acceptance/clients/node-npm12/Dockerfile)).

## Sign a repository

Create the pair, then name it as the repository's primary pair. Naming it queues the
reindex that signs every version the repository already holds; later publishes are
signed as they are indexed.

```sh
curl --fail --user "admin:$ARTIFACTWEAVE_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$ARTIFACTWEAVE_URL/artifactory/api/security/keypair" -d @- <<EOF
{"pairName": "npm-signing", "pairType": "ECDSA", "alias": "npm",
 "privateKey": $(jq -Rs . private.pgp), "publicKey": $(jq -Rs . public.pgp)}
EOF
curl --fail --user "admin:$ARTIFACTWEAVE_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$ARTIFACTWEAVE_URL/artifactory/api/repositories/npm-local" \
  -d '{"primaryKeyPairRef": "npm-signing"}'
npm audit signatures
```

`private.pgp` and `public.pgp` are gpg's armored exports of an ECDSA key, as the 7.x
documentation makes them (`gpg --expert --full-generate-key`, then
`gpg --armor --export-secret-key` and `gpg --armor --export`). A PEM pair works as
well: a PKCS#8 or SEC 1 private key and a `PUBLIC KEY` block, on P-256, P-384 or P-521.

To rotate, create the next pair and name it primary; the old pair may be named
secondary until the queued reindex finishes, so the keys document lists both
meanwhile. Deleting a pair queues nothing: run
`POST /artifactory/api/npm/<repo>/reindex` to remove its signatures.

## The Sigstore dependency

Before it asks the registry, `npm audit signatures` initialises npm's Sigstore TUF
client, which refreshes its metadata from `https://tuf-repo-cdn.sigstore.dev`. Only
when that repository has no keys for the registry, as for any private registry, does
npm fetch `<registry>/-/npm/v1/keys` (npm/cli `lib/utils/verify-signatures.js`).

The refresh needs the network. Checked on 2026-11-12 with npm 11.20.0 and 12.2.1 in
the pinned images: with `--network none`, `npm audit signatures` fails with
`request to https://tuf-repo-cdn.sigstore.dev/<n>.root.json failed` (or
`fetch failed`) before it reaches the registry, with an empty TUF cache and with a warm
one alike. The scenario therefore needs that host, as any npm user's audit does; it
changes nothing in what npm does to avoid it. An outage of the Sigstore CDN fails the
scenario at its first `npm audit signatures` step.

## Run it

```sh
docker build -t artifactweave-accept-node \
  -f test/acceptance/clients/node.Dockerfile test/acceptance/clients
docker build -t artifactweave-accept-npm12 test/acceptance/clients/node-npm12
ARTIFACTWEAVE_NODE_IMAGE=artifactweave-accept-node \
ARTIFACTWEAVE_NPM12_IMAGE=artifactweave-accept-npm12 \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@npm \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1
```

The npm clients reach the binary over the host network, so the scenario runs on a
Linux Docker host. It audits a repository with no pair (npm finds no keys), then
after a PEM pair is named, then after a rotation to a pair given as gpg's armored
export, and last with one version's signature replaced in the store by another
package's, which npm reports as an invalid registry signature.

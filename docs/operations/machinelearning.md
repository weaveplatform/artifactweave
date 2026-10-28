# Machine Learning models with FrogML

ArtifactWeave serves versioned models from `machinelearning` local and one-node
federated repositories. The native client is FrogML 2.1.153. Remote and virtual
repositories are rejected by configuration validation. Cross-peer federation
remains P13; model training, inference and JFrog ML cloud registration are outside
this repository protocol.

## Connect and publish

Create a repository as an administrator:

```sh
curl --fail --user "$AW_USER:$AW_PASSWORD" \
  -H 'Content-Type: application/json' -X PUT \
  "$AW_URL/artifactory/api/repositories/models-local" \
  -d '{"rclass":"local","packageType":"machinelearning"}'
```

Use Python 3.13 and install the pinned client. FrogML 2.1.153 declares Python
`>=3.9,<3.14`; Python 3.14 and 3.15 are outside its supported range.

```sh
python -m pip install frogml==2.1.153
export JF_URL="$AW_URL/artifactory"
export JF_ACCESS_TOKEN="$AW_TOKEN"
```

The token's principal needs Write permission over the model's version and staging
paths to publish, and Read permission over its manifest and every declared
artifact to resolve the model. Scope a permission to
`models/controlled-model/**` for this example.

```python
from frogml import files

files.log_model(
    source_path="model.pkl",
    repository="models-local",
    model_name="controlled-model",
    version="1.0.0",
    properties={"training_dataset": "controlled"},
    register_in_jml=False,
)
info = files.get_model_info("models-local", "controlled-model", "1.0.0")
files.load_model("models-local", "controlled-model", "1.0.0", "./downloaded")
```

`register_in_jml=False` selects the SDK's documented Artifactory-only publication
path. The default also attempts registration with JFrog ML cloud services, which
ArtifactWeave does not provide. A fresh `load_model` validates server SHA-256
headers and downloads the model files into the requested directory. The SDK
uploads an accompanying code archive; code and dependency metadata are also
validated and returned by the manifest endpoint.

## Publication and lifecycle

The SDK starts a transaction at
`POST /artifactory/api/machinelearning/{repo}/model/{name}/start-transaction/{version}`,
uploads bytes to returned paths under `models/{name}/tmp/{version}/{transaction}`,
and commits a multipart manifest at
`PUT /artifactory/api/machinelearning/{repo}/model/entity-manifest/{name}/{version}/{transaction}`.
The version may be omitted when starting a transaction; the server then assigns
one. The storage API also supports one namespace segment before the model name.

Staging transactions belong to their authenticated creator and expire after
24 hours. Their bytes and the stored manifest cannot be downloaded through raw
item, checksum, archive or copy/move shortcuts. Administrators can remove stale
staging trees through the ordinary item lifecycle; expiration rejects further
uploads and commits but does not automatically purge the stored files.

A commit verifies every declared size and SHA-256, then publishes all version
items, properties and the package index in one metadata transaction. A failed
commit leaves the version unpublished. Repeating the same manifest and properties
against the same live transaction succeeds; different content or properties
conflict. This is the model commit contract, not a general API idempotency-key
facility. Starting a new transaction for an already published version returns 409.

Published files use `models/{name}/{version}/{files,dependencies,code}/...`.
The native manifest GET checks current Read permission for every file before
returning download URLs. Deleting any declared file withdraws that model version
from native resolution and the package index. Restoring the complete set or
running the shared reindex lifecycle restores the version. Copy/move cannot
relocate private records; publication uses the native transaction API.

Names and versions use ASCII letters, digits, dot, underscore and hyphen, at most
60 characters per segment. `tmp` is reserved in names; `tmp`, `files`,
`dependencies` and `code` are reserved versions to keep namespaced identities
unambiguous. File paths reject traversal, control characters, query/matrix
separators and the `!/` archive-entry delimiter. Manifests are limited to 4 MiB,
10,000 files, 10 GiB per file and 32 JSON nesting levels. Duplicate and case-aliased
JSON keys are rejected. Additional properties are bounded to 64 KiB and 256 keys.

## Evidence and compatibility boundary

Checked on 2026-10-28 against the [JFrog Machine Learning guide](https://docs.jfrog.com/artifactory/docs/machine-learning-repositories)
and the [FrogML 2.1.153 distribution](https://pypi.org/project/frogml/2.1.153/#files).
The wheel's `storage/artifactory/_artifactory_api.py`, `storage/frog_ml.py`, manifest
models and `sdk/model_version/files` define the verified request sequence. The
SDK sends UTC creation timestamps without a zone suffix; these and RFC3339
instants are accepted. Its wheel SHA-256 is
`7b2804539c5912410cdca55ea6ae89169afb4f1616d0777be0ac42f898bf8663`.

The guide describes ML as a repository structure rather than a package-manager
protocol. `machinelearning` remains ArtifactWeave's existing configuration name;
this implementation claims native SDK interoperability. Comparison against a
chosen live Artifactory build remains the separate [#216 oracle task](https://github.com/weaveplatform/artifactweave/issues/216).

[`machinelearning.feature`](../../test/acceptance/features/machinelearning.feature)
runs real SDK publication and fresh downloads for L/F, plus invalid-credential
reads and writes. Go tests cover namespace/version validation, transaction
ownership, path-scoped permissions, integrity failures, immutable commit replay,
rollback on storage errors, and delete/restore/reindex. The client image pins
Python 3.13.18 by OCI digest and verifies the FrogML wheel before installation.

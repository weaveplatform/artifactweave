# NVIDIA NIM remote qualification

NIM uses two repositories. Create a `nimmodel` **remote** with upstream
`https://api.ngc.nvidia.com`, blank username and an NVIDIA Personal Key in
`password`. The NIM remote defaults to Bearer authentication for that key.
Create a separate Docker remote at `https://nvcr.io/` with username
`$oauthtoken` and the key as password. Give users read access to each
repository independently. NIM local, virtual and federated repositories are
not admitted.

For a NIM 1.3+ container, set `NGC_API_ENDPOINT` and `NGC_AUTH_ENDPOINT` to
`<host>/artifactory/api/nimmodel/<nim-repo>`, `NGC_API_SCHEME` to `https`, and
`NGC_API_KEY` to an artifactweave credential. Pull the corresponding
`<host>/<docker-repo>/<org>/<team>/<image>:<tag>` through the Docker remote.
The model API and Docker registry have separate upstream keys, caches and
permission checks. Do not point the Docker client at the NIM route.

The NIM route supports NVIDIA's documented tag metadata query and path forms,
repository metadata, and versioned model files or ZIPs. The request
`/v2/nim/metadata?resourceId=nim/meta/llama&tag=1.3` identifies the matching
Docker image `nim/meta/llama:1.3`. The adapter does not fetch image layers;
the Docker registry checks their digests and its own grant. NIM metadata is
bounded and validated before caching; malformed or truncated responses never
publish a successful entry. Cache reads, refresh, offline mode and redirects
use the existing P9 remote policy. Cached tag metadata is indexed as
`models/.../.jfrog_nim_model_info.json` with `Nimmodel.name` and
`Nimmodel.versionId` properties, so the existing property search can find a
model by name or version.

Run the controlled tests with `go test ./internal/server/nimmodel ./pkg/repo`.
The `@nimmodel` acceptance scenario uses a controlled NGC response and a
native `crane pull` through the paired Docker remote, then repeats the reads
offline. CI runs the scenario when its pinned native client is available.

NVIDIA documents the API paths and the NIM 1.3+ boundary. JFrog does not
publish the exact NIM container runtime request/response capture; the
Artifactory wire shape, additional `.jfrog` cache artifacts and the remaining
`Nimmodel.*` property mapping remain inferred for
[#216](https://github.com/weaveplatform/artifactweave/issues/216).
The implementation is qualified against a controlled fixture and does not
claim target-version parity yet.

- [JFrog NIM repository guide](https://docs.jfrog.com/artifactory/docs/nvidia-nim-repositories)
- [NVIDIA metadata API](https://docs.nvidia.com/nim/metadata-api/reference/endpoints)
- [NVIDIA model file URLs](https://docs.nvidia.com/ngc/latest/ngc-catalog-user-guide.html#downloading-models-via-wget-curl)

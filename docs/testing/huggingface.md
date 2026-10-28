# Hugging Face repositories

The native endpoint is `https://<host>/artifactory/api/huggingfaceml/<repo>`.
Models and datasets support local, remote, virtual and one-node federated
repositories. The qualified clients are **huggingface_hub 2.3.0** and
**hf_xet 1.7.0**, with wheel SHA256 pins in the quality gate.

Enable Xet on the server with `huggingface.xet_enabled: true` or
`ARTIFACTWEAVE_HUGGINGFACE_XET_ENABLED=true`. The default is false. Git LFS remains
available; when Xet is disabled its token routes return 501 and resolve responses
do not advertise Xet metadata.

```sh
export HF_ENDPOINT="https://<host>/artifactory/api/huggingfaceml/<repo>"
export HF_TOKEN="<artifactory-access-token>"
export HF_HUB_DISABLE_XET=0
hf cp ./model/weights.bin hf://acme/demo/weights.bin
hf download acme/demo --repo-type model --revision main --local-dir ./downloaded-model
```

Use `HF_HUB_DISABLE_XET=1` for the Git LFS compatibility path. Uploads to a virtual
repository require its `defaultDeploymentRepo` to name an included local or
federated member. Reads select one ordered member for the entire revision.

The [Git LFS scenarios](../../test/acceptance/features/huggingface.feature) and
[Xet scenarios](../../test/acceptance/features/huggingface_xet.feature) invoke the
real CLI against a fresh client home for models and datasets, large files, named
revisions and all four repository classes. The Xet proof proxy rejects ordinary
large-file resolve GETs and checks that CAS uploads and chunk downloads occurred.
Remote scenarios repeat the downloads after the controlled upstream stops.
Byte-range checks independently exercise resumable HTTP reads.

Snapshots remain at `models--<owner>--<name>/snapshots/<40-hex-revision>/...`
(or `datasets--...`), with atomic named refs. Xet records live beneath the same
identity's private `.xet` directory. The format hook rebuilds package/version
metadata after writes, deletes and restores. A reconstruction is accessible only
while its committed snapshot file is readable. Generic downloads, checksum reuse,
folder archives, copy/move and non-admin trash relocation cannot expose raw CAS
records. Hub commits also require a verified upload grant for the same caller and
model, or a readable existing file in that model; knowing a global blob SHA is
insufficient.

CAS capabilities expire after five minutes and recheck current credentials,
permissions, virtual membership and selected revision on every request. Xorbs may
bundle files, so signed transfer URLs return only a requested file's authorized
chunk ranges. Parsers bound compressed and expanded sizes and validate keyed
BLAKE3 roots, shard bounds, segment hashes, file lengths and SHA256 before publishing.
The [codec fixtures](../../internal/format/huggingface/xet/testdata/) were captured
from hf_xet 1.7.0; both compressed repeated chunks and incompressible data are covered.

For remotes, upstream metadata and file bytes travel through the existing P9 Hub
HTTP source policy and integrity checks. The server derives native Xet chunks
from that verified artifact cache on demand, including offline reads. A bounded
128-entry memory cache retains only derived metadata, keyed by the current source
generation, item identity and digest. Every use rechecks the live P9 item and
policy; chunk downloads read only their segment rather than rehashing the whole
model. No-store sources use the existing Hub HTTP path and do not advertise Xet,
avoiding repeated transient full-file transfers. No upstream CAS credentials or
second persistent chunk cache are introduced. Remote redirects stay on the configured
origin; for `https://huggingface.co`, the adapter also allows the HTTPS storage
hosts in Hugging Face's [download guide](https://huggingface.co/docs/hub/models-downloading).
The common transport still checks DNS destinations and strips credentials on an
origin change.

The [JFrog guide](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories),
checked 2026-10-28, documents opt-in Xet with the current ML layout and excludes
Spaces. Its older client-version table conflicts with its description of newer
clients always uploading with Xet. Actual released-client qualification is
recorded here rather than treating that table as a client ceiling. The protocol
and storage decision is recorded in [ADR 0032](../research/decisions/0032-package-format-handlers.md).
Legacy layout migration remains [#789](https://github.com/weaveplatform/artifactweave/issues/789),
and cross-peer federation remains P13.

Native CAS status contract (beneath `/artifactory/api/huggingfaceml/{repo}`):

| Route | Successful result | Rejections |
| --- | --- | --- |
| `GET /api/{models,datasets}/{owner}/{name}/xet-{read,write}-token/{revision}` | 200, scoped capability | 400 invalid identity/query; 401/403 credentials or rights; 404 missing revision; 405 remote write; 501 disabled |
| `POST /xet/{kind}/{owner}/{name}/{revision}/v1/xorbs/default/{hash}` | 200, verified xorb | 403 authority; 409 conflicting record; 422 malformed/digest/size |
| `POST .../v1/shards` and `POST .../v2/shards` | 200 JSON / NDJSON after verified reconstruction | 403 authority; 409 conflict; 422 invalid or missing reconstruction |
| `HEAD .../v1/xorbs/default/{hash}` | 200 if present in this model | 400 invalid hash; 403 authority; 404 absent |
| `GET .../v1/chunks/{prefix}/{hash}` | 404 supported dedup miss | 400 invalid hash/prefix; 403 authority |
| `GET .../v1/reconstructions/{hash}` and `GET .../v2/reconstructions/{hash}` | 200 terms and scoped chunk URLs | 400 invalid hash; 403 authority; 404 unpublished/denied file; 416 invalid range; 422 corrupt content |
| `GET .../data/{hash}/{segment}/{start}/{end}` | 200 or 206 encoded chunks | 400 invalid/unowned chunk range; 403 authority; 404 absent file; 416 invalid byte range; 422 corrupt data |

Storage/source failures use the existing shared HTTP error mapping and never
return successful metadata or partial published reconstruction mappings.

# Hugging Face repositories

The native `huggingfaceml` route is `https://<host>/artifactory/api/huggingfaceml/<repo>`. Set `HF_ENDPOINT` to that URL and authenticate with an Artifactory access token. The pinned acceptance client is the `hf` command from `huggingface_hub` 2.3.0, with Xet disabled while the separate Xet CAS protocol remains pending. Go [acceptance steps](../../test/acceptance/huggingface_steps_test.go) invoke the real CLI's `upload` and `download` commands to publish and download a model and a dataset, including a file over 1 MiB sent through Git LFS. The [fixtures](../../test/acceptance/testdata/huggingface/) live under the format directory. The scenarios run against local and one-node federated repositories, resolve through a virtual member, and download remote content again after the fixture upstream stops. [Server tests](../../internal/server/huggingface/protocol_test.go) check wire metadata, signed upload tampering, immutable revision conflict, virtual isolation and lifecycle.

The Go steps check the native revision tree and request a byte range of each large file. That checks the resumable HTTP download path separately from the full snapshot transfer. CI downloads the client wheels by pinned version and verifies their SHA256 before running this feature.

```sh
export HF_ENDPOINT="https://<host>/artifactory/api/huggingfaceml/<repo>"
export HF_TOKEN="<artifactory-access-token>"
export HF_HUB_DISABLE_XET=1
hf upload acme/demo ./model . --repo-type model
hf download acme/demo --repo-type model --revision main --local-dir ./downloaded-model
```

Models and datasets are separate identities. Each immutable revision stores files under `models--<owner>--<name>/snapshots/<40-hex-revision>/...` or the corresponding `datasets--` prefix. `main` and safe named revisions such as `v1` resolve through atomic ref records to a full revision; older snapshots without a `main` record resolve to their latest revision. The format hook rebuilds the package/version view after writes, deletes and restores; explicit reindex reconstructs it from snapshot files. Local and one-node federated writes are supported. A virtual repository selects one ordered member for a whole revision, so a client cannot assemble one snapshot from multiple members. A remote repository validates upstream model metadata, Git blob/LFS identities and source policy before cache publication. Its metadata and files are subject to read rights and cache refresh rules.

Remote file redirects remain on the configured upstream origin. When the upstream is `https://huggingface.co`, the adapter also permits the HTTPS storage hosts in Hugging Face's [current model download allowlist](https://huggingface.co/docs/hub/models-downloading). The common remote transport still checks DNS destinations and strips credentials at an origin change. The server tests cover a same-origin redirect, a denied unrelated host and a corrupt redirected file.

The [current JFrog Hugging Face guide](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories), checked 2026-10-25, documents local, remote and virtual models and datasets, the current ML layout, and an opt-in Xet CAS transfer path. It says `huggingface_hub` up to 1.18.0 and `hf_xet` 1.4.3 are supported, with 1.19.0 for SaaS 7.173. It also states that Spaces are currently unsupported. That is a statement about the documented target, not a permanent product decision. INV-01 additionally approves one-node federated behavior; cross-peer convergence remains P13.

Git LFS is the current native large-file path. The opt-in Xet CAS token, xorb/chunk and reconstruction protocol is a separate P9c blocker for current-guide parity; the token route currently returns 501 and no Xet metadata is advertised. The deprecated layout migration remains [#789](https://github.com/weaveplatform/artifactweave/issues/789). Do not mark the format fully native in the [inventory](../implementation/package-format-matrix.md) until Xet and the combined-tree qualification are complete.

# Migrate a legacy Hugging Face revision

This administrator operation copies one verified legacy revision into the
current layout and retains its originals. It is an artifactweave extension,
not an implementation claim for JFrog's undocumented upgrade API.

Supported source grammar: `models/<owner>/<name>/<ref>/<timestamp>/<file>` or
`datasets/<owner>/<name>/<ref>/<timestamp>/<file>`, in a local or one-node
federated Hugging Face repository. Destination:
`models--<owner>--<name>/snapshots/<40-hex-revision>/<file>` (or `datasets--...`).
Model identity, revision identity, actual and declared checksums, artifact
properties and large-file SHA256/LFS identity are preserved. Source items are
not deleted. Current-layout Xet records are not moved or rewritten.

Before migrating, pause repository writes, permission/configuration changes and
federation synchronization. Back up the source export. Federated peer migration
and cross-peer convergence remain P13 work. Confirm the exact revision from the
export: the source `.jfrog_huggingface_model_info.json` (dataset variant for
datasets) must carry exactly that value in its
`huggingfaceml.generated.revision.sha1` item property. No metadata JSON schema is
assumed and no revision is generated from file contents.

Create a reviewed mapping file:

```json
{
  "layout": "jfrog-7.77-timestamp-v1",
  "kind": "models",
  "owner": "acme",
  "name": "demo",
  "source_ref": "main",
  "timestamp": "1700000000",
  "revision": "0123456789012345678901234567890123456789",
  "ref": "main",
  "dry_run": true
}
```

Use an administrator credential to submit it. A scoped administrator token must
also grant READ on every source item, including the revision metadata, and
READ/DEPLOY on every destination file and optional named ref. These checks also
apply to dry-run and completed-migration status requests; administrator status
does not bypass artifact path scopes.

```sh
curl --fail-with-body --user "$AW_USER:$AW_TOKEN" \
  -H 'Content-Type: application/json' \
  --data-binary @migration.json \
  "$AW_URL/artifactory/api/huggingfaceml/$AW_REPO/api/artifactweave/migrations/legacy-layout"
```

`dry_run: true` verifies all source bytes, checks the destination and compares
permission applicability without publishing. A 200 response reports `ready`
or `complete`, source and destination paths, revision, file/byte counts,
`verified_files` and `published_files`. Set `dry_run` to `false` to apply the
same mapping. The synchronous operation reports progress at revision granularity;
there is no detached background task. Migrate historical revisions first without
`ref`, then the desired current revision with its named ref.

One request accepts at most 8 KiB of JSON, 1,024 source/destination tree items
(including folders and internal metadata), 16 GiB of source payloads, and a
scan of at most 1,024 permission documents. Larger revisions or policy stores
are refused; do not split a single revision into partial migrations. Streaming
hash verification bounds memory independently of payload size. Use one request
per revision and allow enough HTTP time for its byte count.

The operation checks every permission's applicable target union on each mapped
file and the model/revision folders. If moving the path grants or removes an
action for a user, group or anonymous identity, it returns 409. Review and update
the policy to cover both layouts equivalently before retrying. Permission changes
are never automatic. Existing token path scopes are not rewritten: reissue
legacy-path-scoped tokens for the reviewed current paths as needed. A token
still cannot bypass the underlying permission grants.

On completion, read both revision metadata and actual bytes with the pinned
client and a fresh home, and repeat with an account that should be denied:

```sh
export HF_ENDPOINT="$AW_URL/artifactory/api/huggingfaceml/$AW_REPO"
export HF_TOKEN="$AW_TOKEN"
export HF_HOME="$(mktemp -d)"
export HF_HUB_DISABLE_XET=1
hf download acme/demo --revision 0123456789012345678901234567890123456789 \
  --local-dir migrated-demo --max-workers 1
```

Compare exported byte hashes, then resume normal writes after reviewing results.
Legacy files have no fabricated Xet metadata: ordinary Hub/LFS downloads remain
available, and existing verified Xet records for the same model and SHA256 remain
usable. This operation does not convert raw weights into new Xet CAS records.

## Recovery and conflicts

If the request fails or its response is lost, repeat the identical mapping with
`dry_run: true`. An identical complete destination returns `complete`; a rolled
back destination returns `ready`. Reapplying a complete migration does not change
items or roll a subsequently advanced ref back. A named ref is only initialized
during first publication; add new refs through the native branch API afterward.

Any unexpected destination file, different bytes/properties, partial migrated
tree or conflicting initial ref returns 409 and leaves both layouts untouched.
Investigate the competing content and source export rather than deleting or
overwriting it automatically. Storage failures and request cancellation cannot
publish a successful partial revision; committed destination files, package index
and optional ref share one transaction. Original source files remain available
for operator recovery and are not removed by a successful rerun.

Unsupported: pre-7.77 timestamp-free paths, ownerless names, absent or ambiguous
revision properties, unrecognized internal metadata files, remote/virtual
repositories, Spaces, larger-than-bound revisions, missing payload blobs and
pointer-only exports. The operation refuses these cases rather than guessing a
mapping, fetching unverified content or silently changing transport semantics.

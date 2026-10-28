# Native Xet fixtures

Captured from released `hf_xet` 1.7.0 on 2026-10-28 using
`XetSession.new_upload_commit`, `start_upload_file(..., sha256=COMPUTE_SHA256)`
and a controlled local CAS endpoint. The files contain the original POST bodies,
without conversion through artifactweave's encoder.

- `native.xorb` / `native.shard`: two files, Python `random.Random(779).randbytes(10000)`
  and `bytes(range(256))*100`. This covers incompressible and compressed chunks,
  multiple files, SHA256 metadata and segment verification hashes.
- `large.xorb` / `large.shard`: one file, `bytes(range(256))*5000` (1,280,000 bytes).
  Repeated chunks deduplicate within the xorb and recur in reconstruction terms.
  Its Xet file hash is `869fb7b9851f0c53b04e946381db72f24d74462c24cbe4737856e41992386def`.

Native uploads use bare chunk records and minimal version-2 MDB shard sections.
The tests additionally construct indexed footer variants and ordinary/byte-grouped
LZ4 frames to verify bounds and corruption handling independently.

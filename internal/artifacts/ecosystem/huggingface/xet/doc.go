// Package xet validates the public Hugging Face Xet chunk and shard wire
// formats: a xorb (a run of chunks, each optionally LZ4-compressed, with an
// optional footer of chunk hashes and boundaries) and a version-2 shard (the
// files an upload adds, each mapped to xorb chunk ranges, and the xorbs it
// claims). It computes Xet's keyed BLAKE3 hashes (a chunk's data hash, a
// xorb's Merkle root, a file's hash and a segment's verification hash) bit
// for bit as hf_xet does, since clients name xorbs and files by them.
//
// # Role
//
// artifacts module, ecosystem layer: Xet chunk, xorb and shard wire
// validation for the Hugging Face slice. It holds no state and reaches no
// store: the slice's protocol (artifacts/ecosystem/huggingface/http) reads
// and writes the records, and the format package (artifacts/ecosystem/huggingface)
// checks a record path's hash grammar with ParseHash.
//
// # Inputs
//
//   - Driving: the protocol, which parses an uploaded xorb against the hash
//     it is uploaded under (ParseXorb), parses an uploaded shard
//     (ParseShard), checks each file a shard maps by its hash and its
//     segments' verification hashes (FileHash, RangeHash, Parts), derives
//     chunks and hashes for a remote repository's cached files (DataHash,
//     Aggregate, FileHash) and encodes the chunks a signed range answers
//     (EncodeChunks); and the format package, which checks a record path's
//     hash (ParseHash).
//   - Driven: nothing but BLAKE3 and LZ4 decoding.
//
// # Outputs
//
//   - Hash with String and ParseHash, HashPart, DataHash, Aggregate,
//     FileHash and RangeHash.
//   - Xorb, Chunk, ParseXorb, Parts and EncodeChunks; Shard, File, Segment,
//     ShardXorb and ParseShard.
//   - The bounds every caller enforces before reading a body: MaxChunk,
//     MaxXorb, MaxEncodedXorb, MaxChunks and MaxShard.
//   - ErrInvalid, which every refusal wraps with its reason.
//
// # Invariants
//
//   - Every length and count is bounded before it is used, so a hostile
//     xorb or shard cannot make a parse allocate or loop past the limits.
//   - A xorb is accepted only when every chunk decodes, both of its footer's
//     tables (when it has one) agree with the chunks, and the chunks' root
//     is the hash it is named by; a shard only when each xorb it claims adds
//     up to its size and root. A shard's lookup tables are bounded and never
//     trusted.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose cached files the protocol chunks).
//   - docs/testing/huggingface.md ("Xet", the codec fixtures captured from
//     hf_xet 1.7.0) and docs/operations/huggingface-migration.md.
//   - JFrog Hugging Face repositories, which document opt-in Xet:
//     https://docs.jfrog.com/artifactory/docs/hugging-face-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/huggingface) and line 1006 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/779 and
//     https://github.com/weaveplatform/artifactweave/issues/1036
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Moved here in P9d.
//
// # Testing
//
//   - codec_test.go: the native fixtures in testdata (xorbs and shards
//     captured from hf_xet 1.7.0) parsed and reconstructed, the hash grammar
//     and known roots, indexed footers, compression (byte-grouped LZ4
//     included) and limits, and shard bounds and duplicate records;
//     FuzzXetWireParsing runs its seeds in every unit run (Makefile
//     FUZZ_PKGS).
package xet

// Package xzfile reads whole .xz files, as liblzma and xz -t read them,
// with a dictionary no larger than its block header names or the caller
// reads, and none above 1.5 GiB, as XZ for Java reads them.
//
// # Role
//
// platform layer: xz decoding that no module owns. In the artifacts
// module, every package format that admits xz content reads it here: Nix
// NARs and channel tarballs, Debian package indexes and a package's
// control.tar.xz and data.tar.xz, CRAN .tar.xz source packages, p2
// .xml.xz metadata and Terraform's remote .tar.xz and .xz modules. It
// keeps github.com/ulikunitz/xz's LZMA2 decoder (its lzma package) and
// parses the container itself, because that package's Reader ends without
// an error where a block header or the index must start, and allocates
// whatever dictionary a block header names. It imports the standard
// library and that lzma package, and no module.
//
// # Inputs
//
//   - The file, as an io.Reader read once from its first byte.
//   - The limit: the most bytes the caller reads from the content, or 0
//     for none.
//
// # Outputs
//
//   - An io.Reader of the content of every stream in the file, in order.
//     Its Read returns io.EOF only once the input ended after a stream's
//     footer and its stream padding.
//   - A refusal: io.ErrUnexpectedEOF for an input that ends anywhere but
//     between streams, and otherwise an error whose text is the one
//     ulikunitz/xz's Reader gives the same fault, such as "xz: checksum
//     error for block". Errors of the LZMA2 decoder pass as it gives them.
//   - MaxDict, the largest dictionary a block header may name.
//
// # Invariants
//
//   - The container is checked as the .xz file format 1.2.1 sets it out:
//     the stream header and its CRC32, each block header and its CRC32,
//     one LZMA2 filter, the sizes a block header declares, block padding,
//     each block's check (none, CRC32, CRC64 or SHA-256), an index that
//     lists exactly the blocks read, its padding and CRC32, a footer that
//     names the index's size and the stream's check type, stream padding
//     in groups of four null bytes, and concatenated streams.
//   - The end of the input is an error anywhere but between streams. A file
//     whose index and footer were cut off is refused, as liblzma refuses it
//     with "Unexpected end of input" (#1297, #1331).
//   - A block header whose dictionary property is above 37, a dictionary
//     above MaxDict (1.5 GiB), is refused before anything is allocated,
//     with ulikunitz/xz's text for a property above 40. Artifactory decodes
//     xz with XZ for Java, which refuses it the same way; ulikunitz/xz
//     allocated up to 4 GiB first for properties 38 to 40 (#1332).
//   - Each block is decoded with the dictionary its header names, as XZ for
//     Java and liblzma allocate it, lowered to the caller's limit and raised
//     to the LZMA2 decoder's 4 KiB. A match cannot reach further back than
//     the content decoded, so the limit changes nothing the caller reads.
//     Data that refers further back than its header names is refused, as
//     both refuse it. ulikunitz/xz read it within an 8 MiB dictionary, and
//     within 64 MiB for Nix and Terraform.
//   - For a fault ulikunitz/xz's Reader refused, the refusal reads as it
//     did there, in the same order of checks, so Debian's 400 for a package
//     (which names the fault) is unchanged. Where that Reader decoded a file
//     whose input ended early, or, for a field that runs past its block
//     header, ended the stream there and read on as a new stream, the
//     refusal is io.ErrUnexpectedEOF.
//
// # References
//
//   - The .xz file format 1.2.1: https://tukaani.org/xz/xz-file-format.txt.
//   - liblzma's stream decoder, which xz -t, dpkg, apt, R and libarchive
//     (Nix) decode with:
//     https://github.com/tukaani-project/xz/blob/master/src/liblzma/common/stream_decoder.c.
//   - XZ for Java, which Artifactory decodes xz with through Apache Commons
//     Compress (RTFACT-29375's stack trace, 7.41.6): LZMA2Decoder refuses a
//     dictionary property above 37,
//     https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/LZMA2Decoder.java#L15,
//     LZMA2InputStream allocates the dictionary the property names,
//     https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/LZMA2InputStream.java#L169,
//     and LZDecoder refuses a match past it,
//     https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/lz/LZDecoder.java#L91.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (package format handlers).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - https://github.com/weaveplatform/artifactweave/issues/1297,
//     https://github.com/weaveplatform/artifactweave/issues/1331 and
//     https://github.com/weaveplatform/artifactweave/issues/1332
//
// # Status
//
// Implemented after P9d: the reader Nix's whole-stream fix added in P9c
// (#1297), moved here so Debian, CRAN, p2 and Terraform read xz with it too
// (#1331), and its dictionary bounded as XZ for Java bounds it (#1332). A
// format whose limit is large, such as Nix's 2 GiB, can still allocate up
// to 1.5 GiB for a header of a few bytes; no memory budget is shared
// between decodes yet.
//
// # Testing
//
//   - reader_test.go: TestUnitDecodesWholeFiles (each check type, declared
//     sizes, several blocks, concatenated streams with padding, and
//     ulikunitz/xz's own writer), TestUnitRefusesAnEarlyEnd (every early
//     end of a fixture, in ulikunitz/xz's words where it refused it and as
//     io.ErrUnexpectedEOF where it did not), TestUnitRefusesAsUlikunitzDoes
//     (every byte of the fixture changed, against ulikunitz/xz),
//     TestUnitRefusesBrokenParts (each part of the container broken, with
//     ulikunitz/xz's text), TestUnitDictionarySize (the header's
//     dictionary, the limit and the 4 KiB least),
//     TestUnitDictionaryProperty (every property byte, and the refusal
//     above 37), TestUnitAHugeDictionaryIsNotAllocated (headers that name
//     more than 1.5 GiB are refused before any allocation, and one that
//     names 1.5 GiB allocates no more than the limit) and
//     TestUnitDataBeyondTheDictionary (data that refers past the
//     dictionary, or past what its header names, is refused, and read
//     whole with one that reaches).
//   - Each format's tests read a fixture cut by its index and footer:
//     ecosystem/nix (xz_test.go), ecosystem/debian (xz_test.go),
//     ecosystem/cran (xz_test.go), ecosystem/p2 (xz_test.go) and
//     ecosystem/terraform/http (remote_archive_xz_test.go), and the server
//     suites under test/server/artifacts send them through the routes.
package xzfile

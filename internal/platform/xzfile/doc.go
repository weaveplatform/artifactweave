// Package xzfile reads whole .xz files, as liblzma and xz -t read them,
// with a dictionary no larger than its block header names or the caller
// reads, and none above 1.5 GiB, as XZ for Java reads them, allocated as
// it fills, within a memory budget that the dictionaries of all decodes
// share.
//
// # Role
//
// platform layer: xz decoding that no module owns. In the artifacts
// module, every package format that admits xz content reads it here: Nix
// NARs and channel tarballs, Debian package indexes and a package's
// control.tar.xz and data.tar.xz, CRAN .tar.xz source packages, p2
// .xml.xz metadata and Terraform's remote .tar.xz and .xz modules. It
// decodes LZMA2 with lzma2, github.com/ulikunitz/xz's decoder copied so
// that its dictionary grows as it fills (decision 0045), and parses the
// container itself, because that package's Reader ends without an error
// where a block header or the index must start, and allocates whatever
// dictionary a block header names. It imports the standard library and
// lzma2, and no module. The app sets the budget from
// xz.dictionary_budget, and marks each HTTP request with WithRequest.
//
// # Inputs
//
//   - The caller's context, which ends a wait for the budget, and may
//     carry the request WithRequest marks.
//   - The file, as an io.Reader read once from its first byte.
//   - The limit: the most bytes the caller reads from the content, or 0
//     for none.
//   - The budget, SetBudget: the most bytes of dictionary the process's
//     decodes hold at once, or 0 for none. DefaultBudget is 1 GiB.
//
// # Outputs
//
//   - An io.ReadCloser of the content of every stream in the file, in
//     order. Its Read returns io.EOF only once the input ended after a
//     stream's footer and its stream padding. Close gives back the
//     dictionary it holds.
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
//     Java and liblzma size it, lowered to the caller's limit and raised
//     to the LZMA2 decoder's 4 KiB. A match cannot reach further back than
//     the content decoded, so the limit changes nothing the caller reads.
//     Data that refers further back than its header names is refused, as
//     both refuse it. ulikunitz/xz read it within an 8 MiB dictionary, and
//     within 64 MiB for Nix and Terraform.
//   - The dictionary is allocated as it fills, where XZ for Java allocates
//     all of it first: 64 KiB, or all of it if that is smaller, then at
//     least twice as much each time it is full, up to its size (#1413). A
//     file of a few bytes takes 64 KiB whatever its header names. Every
//     answer is the one a dictionary allocated in full gives: a match past
//     the content decoded is refused in the same words.
//   - A block takes each allocation of its dictionary from the budget
//     before it is made, as the block starts and as the dictionary grows,
//     and gives all of it back when the block's data ends, when Read fails
//     or returns io.EOF, and on Close; a reader dropped unclosed gives it
//     back once the garbage collector finds it. The budget counts the
//     dictionary only, not the 32 KiB input buffer, nor a dictionary just
//     outgrown, which the garbage collector frees.
//   - A decode that does not fit, as it starts or as it grows, waits, in
//     order of arrival, and is never refused: XZ for Java decodes with no
//     memory limit (Apache Commons Compress passes -1), so a refusal would
//     change an answer (#1354). One that fits goes ahead whatever its
//     context says, as it did without a budget; a wait its context ends
//     fails Read with the context's error and takes nothing more.
//   - A dictionary larger than the whole budget decodes alone: it goes in
//     once nothing else holds the budget, and those behind it wait.
//   - A decode that grows holds part of the budget, so it passes the
//     decodes queued to start. When every holder inside waits for more,
//     none of them can give any back, so the one that has waited longest
//     grows alone past the budget, and no other goes past it until that
//     one holds nothing: the decodes together hold at most the budget and
//     one dictionary.
//   - The readers of one request (WithRequest) are one holder. One that
//     already holds part of the budget passes the decodes queued ahead of
//     it, and goes in while it alone holds the budget, so a request that
//     reads a second xz file while its first is open cannot wait on
//     itself. Readers of no request are each a holder of their own.
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
//   - Decision 0045: docs/research/decisions/0045-lzma2-growing-dictionary.md
//     (the decoder copied so its dictionary grows), and lzma2's doc.go.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - https://github.com/weaveplatform/artifactweave/issues/1297,
//     https://github.com/weaveplatform/artifactweave/issues/1331,
//     https://github.com/weaveplatform/artifactweave/issues/1332 and
//     https://github.com/weaveplatform/artifactweave/issues/1354
//   - The dictionary that grows as it fills, so a short file takes less
//     than its header names: https://github.com/weaveplatform/artifactweave/issues/1413
//
// # Status
//
// Implemented after P9d: the reader Nix's whole-stream fix added in P9c
// (#1297), moved here so Debian, CRAN, p2 and Terraform read xz with it too
// (#1331), its dictionary bounded as XZ for Java bounds it (#1332), the
// dictionaries of all decodes held within a shared budget (#1354), and
// each dictionary allocated, and charged to the budget, as it fills
// (#1413).
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
//   - grow_test.go: TestUnitAShortFileTakesLittle (a 52-byte file whose
//     header names 1.5 GiB decodes with 64 KiB of dictionary, by the
//     budget and runtime.MemStats, at Nix's and Debian's limits and with
//     none), TestUnitTheBudgetHoldsWhatTheDictionaryHolds (and a short
//     file beside it decodes at once), TestUnitAGrowingDictionaryWaitsForRoom,
//     TestUnitStalledDecodesGrowOneAtATime,
//     TestUnitACancelledGrowthGivesBackItsDictionary and
//     TestUnitMatchesReachTheWholeDictionary (matches that reach back the
//     whole dictionary decode as ulikunitz/xz's reader decodes them, and
//     one that reaches further is refused in its words).
//   - budget_test.go: TestUnitTheBudgetBoundsConcurrentDecodes (sixteen
//     decodes at once of files whose header names 1.5 GiB and whose
//     content fills 1 MiB hold no more than the budget and one dictionary,
//     by the budget and runtime.MemStats, and all of theirs without it);
//     the tests of the queue lower the dictionary below the 64 KiB a block
//     starts with, so it is taken whole as the block starts:
//     TestUnitADecodeThatDoesNotFitWaits,
//     TestUnitADictionaryLargerThanTheBudgetDecodesAlone,
//     TestUnitACancelledWaitTakesNothing, TestUnitOneRequestReadsTwoStreams
//     (no deadlock with WithRequest, and the wait without it),
//     TestUnitTheDictionaryIsGivenBack (block end, io.EOF, an error, Close
//     and the garbage collector), TestUnitTurningTheBudgetOffLetsWaitersIn
//     and TestUnitSameAnswersWithTheBudgetOnAndOff (every early end and
//     changed byte of the fixture).
//   - Each format's tests read a fixture cut by its index and footer:
//     ecosystem/nix (xz_test.go), ecosystem/debian (xz_test.go),
//     ecosystem/cran (xz_test.go), ecosystem/p2 (xz_test.go) and
//     ecosystem/terraform/http (remote_archive_xz_test.go), and the server
//     suites under test/server/artifacts send them through the routes.
//     Each format's xz_budget_test.go reads its xz fixtures with a budget
//     smaller than their dictionaries and with none, and gets the same
//     answers.
package xzfile

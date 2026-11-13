# 0045: An LZMA2 decoder whose dictionary grows as it fills

Status: Proposed

Tracked by [#1413](https://github.com/weaveplatform/artifactweave/issues/1413).

## Context

Every package format that admits xz content reads it through `internal/platform/xzfile`:
Nix NARs and channel tarballs, Debian indexes and a package's `control.tar.xz` and
`data.tar.xz`, CRAN `.tar.xz` source packages, p2 `.xml.xz` metadata and Terraform's
remote `.tar.xz` and `.xz` modules. Each xz block names the LZMA2 dictionary it was
written with, up to 1.5 GiB (property 37) since
[#1332](https://github.com/weaveplatform/artifactweave/issues/1332), and the reader
lowers it to the most the format reads.

The LZMA2 decoder was `github.com/ulikunitz/xz/lzma` v0.5.17. Its `NewReader2` builds the
dictionary with `newBuffer`, which runs `make([]byte, size+1)` before a byte is decoded.
A 52-byte file whose header names 1.5 GiB therefore allocated 1.5 GiB for Nix, Debian
`data.tar.xz` and Terraform remote modules, and 1 GiB for CRAN. Go counts the whole slice
in the heap whether or not its pages are touched, so the garbage collector's pacer and
`GOMEMLIMIT` see all of it, and a span the runtime reuses is zeroed in full.
[#1354](https://github.com/weaveplatform/artifactweave/issues/1354) bounded what the
decodes of a process hold at once (`xz.dictionary_budget`), but each such decode still
took, and waited for, the whole size.

Artifactory decodes xz with XZ for Java, whose
[`LZMA2InputStream`](https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/LZMA2InputStream.java#L169)
also allocates the dictionary its header names in full. How much memory a decode takes is
not an answer a client sees: a match that reaches past the content decoded is refused
whatever the dictionary's size, and a match within it reads the same bytes.

## Decision

- **The decoder is a copy.** `internal/platform/xzfile/lzma2` holds the decoder side of
  `github.com/ulikunitz/xz/lzma` v0.5.17, under that module's BSD licence, which
  `LICENSE` in the package holds; each copied file keeps its copyright notice and names
  the upstream file. The package's `doc.go` notes the upstream version and lists every
  change. The encoder, the classic `.lzma` reader and writer and `Writer2` are not copied.
- **The dictionary grows as it fills.** It starts with 64 KiB, or the whole dictionary if
  that is smaller, and at least doubles, copying what it holds, each time a write would
  not fit, up to the dictionary the block header names, lowered to the caller's limit and
  raised to 4 KiB, as before. Until it reaches that size its ring has never wrapped, so
  the bytes it holds keep their places in the larger one; from then on it wraps as
  upstream's does.
- **No answer changes.** The room left is counted against the whole dictionary, so the
  decoder decodes as far ahead of each read as upstream's does, and a match is checked
  against the whole dictionary and the content decoded since the last reset. Every read
  gives the same bytes and every refusal the same text, `writeMatch: distance out of
  range` for a match past the content decoded among them.
- **The budget charges what the dictionary holds.** `Reader2Config.Grow` is called with
  each capacity before it is allocated, the first included, and `xzfile` takes the
  difference from the dictionary budget there, waiting while it does not fit. A short file
  therefore takes 64 KiB from the budget, not 1.5 GiB. A decode that waits to grow holds
  what it has grown to.
- **Decodes that grow at once never wait on each other for ever.** A growing decode is a
  holder already inside the budget, so it passes decodes queued to start, as a request's
  second reader did (#1354). When every holder inside waits for more, none of them gives
  any back, so the one that has waited longest goes in past the budget, alone: no other
  goes past it until that one holds nothing.
- **The module stays a dependency.** The xz and LZMA2 writers in the p2 composite metadata
  and the Debian index builder, Debian's `.lzma` index reader and the tests that write xz
  fixtures still use `github.com/ulikunitz/xz`, so `go.mod` keeps it.

## Rationale

The issue offered two ways: copy the decoder with the change, or ask upstream for a
`Reader2Config` option that grows the dictionary and take the release that has it. The
copy fixes the allocation now and changes only this repository. Asking upstream needs the
maintainer's go-ahead to post there, and its timing is upstream's.

Other ways were weighed and set aside:

- **Keep allocating in full.** The budget bounds the total, but a file of a few bytes
  still costs, and waits for, 1.5 GiB of it.
- **Size the dictionary from the block header's uncompressed size.** The size is optional:
  a writer that streams writes the header before the data and leaves it out, as
  ulikunitz/xz's writer does and xz does on one thread, so those files would still
  allocate in full.
- **Another decoder.** Its refusals would read differently, and Debian names the fault in
  its 400 for a package (`internal/platform/xzfile`, Invariants), so a package refused
  before would be refused in other words.

Growing the dictionary departs from how XZ for Java allocates it, but it changes no answer
a client receives, so it is no deviation from Artifactory's behaviour and needs no entry in
[deviations.md](../../implementation/deviations.md).

A dictionary that grows charges the budget while the decode is under way, so decodes can
wait on each other where before none waited while it held anything. Letting the longest
waiter in alone past the budget is what keeps them from waiting for ever. The bound on
what the decodes hold together is therefore the budget plus one dictionary, where it was
the larger of the two; in return a decode holds only what its content fills.

## Constraints

- The copy does not follow upstream. When Dependabot moves `github.com/ulikunitz/xz` to a
  release that changes `lzma/`'s decoder files, the change is ported by hand and the
  version noted in `lzma2/doc.go` is updated. A newer module does not change the decoder
  until then.
- The doubling copies fewer bytes, over a block's whole growth, than the dictionary
  reaches. `BenchmarkReader2` in the package measures the cost against upstream's decoder
  for 64 MiB of content with a 64 MiB dictionary: when this decision was made the two took
  the same time within the noise of the machine, and the growing one allocated 64 MiB more
  over the decode, the dictionaries it outgrew.
- The budget does not count the dictionary a decode has just outgrown, which lives until
  the garbage collector frees it.
- A request whose readers run on more than one goroutine can be let past the budget
  sooner than it needed: it waits for more on one goroutine while another of its readers
  still runs. That costs memory, never a wait.

## Verification

- `internal/platform/xzfile/lzma2`: upstream's tests of the decoder side, renamed
  `TestUnit*`; a growing buffer answers every write, read and room check as a full one
  does; `Grow`'s capacities and its refusal; and `Reader2` against upstream's, read by
  read, for streams of text, random bytes, matches across the growth, to the whole
  dictionary and past it, uncompressed chunks and dictionary resets, each also cut short
  and with bytes changed.
- `internal/platform/xzfile` (`grow_test.go`): a 52-byte file whose header names 1.5 GiB
  decodes with 64 KiB of dictionary at the Nix and Debian limits and with none; the budget
  holds what the dictionary has grown to; a decode that must grow waits for room; two that
  stall grow one at a time; a cancelled wait to grow gives back the dictionary; and a block
  whose matches reach back its whole dictionary decodes as ulikunitz/xz's reader decodes it,
  with one that reaches further refused in its words. The existing reader and budget tests
  answer as before.
- Each format's xz tests (`ecosystem/nix`, `ecosystem/debian`, `ecosystem/cran`,
  `ecosystem/p2`, `ecosystem/terraform/http`) and the server suites under
  `test/server/artifacts` give the same answers.

## References

- Upstream: <https://github.com/ulikunitz/xz/tree/v0.5.17/lzma> (BSD licence,
  <https://github.com/ulikunitz/xz/blob/v0.5.17/LICENSE>).
- XZ for Java `LZMA2InputStream`, which allocates the dictionary in full:
  <https://github.com/tukaani-project/xz-java/blob/f14a6039/src/org/tukaani/xz/LZMA2InputStream.java#L169>.
- The .xz file format 1.2.1: <https://tukaani.org/xz/xz-file-format.txt>.
- [#1332](https://github.com/weaveplatform/artifactweave/issues/1332),
  [#1354](https://github.com/weaveplatform/artifactweave/issues/1354) and
  [#1413](https://github.com/weaveplatform/artifactweave/issues/1413).
- `internal/platform/xzfile/doc.go` and `internal/platform/xzfile/lzma2/doc.go`.

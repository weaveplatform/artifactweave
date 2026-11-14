// Package lzma2 decodes LZMA2 chunk sequences, the data of an xz block,
// with a dictionary that grows as it fills. It is the decoder side of the
// lzma package of github.com/ulikunitz/xz v0.5.17, copied under that
// module's BSD licence, which LICENSE in this directory holds; each copied
// file keeps its copyright notice. Status lists what changed.
//
// # Role
//
// platform layer: the LZMA2 decoder of platform/xzfile, which decodes
// every xz block the artifacts module reads. It imports the standard
// library only.
//
// # Inputs
//
//   - The LZMA2 data of one block, as an io.Reader read from its first
//     chunk.
//   - Reader2Config: DictCap, the most the dictionary holds, which xzfile
//     sets to what the block header names, lowered to the caller's limit;
//     and Grow, called with each capacity before the dictionary is
//     allocated to it.
//
// # Outputs
//
//   - Reader2, whose Read gives the decoded content and io.EOF after the
//     end-of-stream chunk.
//   - The errors ulikunitz/xz's Reader2 gives, in its words, such as
//     "writeMatch: distance out of range" for a match that reaches past
//     the content decoded, and Grow's own error when it refuses the room.
//
// # Invariants
//
//   - The dictionary starts with a capacity of 64 KiB, or DictCap if that
//     is smaller, and at least doubles each time a write would not fit, up
//     to DictCap (#1413). Until it reaches DictCap its ring has never
//     wrapped, so the bytes it holds keep their indices in the larger one;
//     from then on it wraps as upstream's does. The bytes copied over all
//     the growth are fewer than the capacity reached.
//   - Every answer is upstream's: the same bytes, the same errors, and the
//     same amount decoded ahead of each read, because the room left is
//     counted against DictCap, not against what the dictionary has grown
//     to. A match is checked against DictCap and the content decoded since
//     the last dictionary reset, as upstream checks it, and the bytes it
//     reads are always ones the dictionary holds.
//   - Grow is called before each allocation, the first one included, so a
//     caller that charges a budget in it has charged what the dictionary
//     holds before it holds it. When Grow fails nothing is allocated or
//     written, and NewReader2 or the Read returns its error.
//
// # References
//
//   - Upstream: https://github.com/ulikunitz/xz/tree/v0.5.17/lzma, module
//     github.com/ulikunitz/xz v0.5.17 (go.sum h1:flR0y/x1hgM8EGV1AW3Xll6T413G0glV8UfBwR617V4=).
//   - The LZMA2 format: the .xz file format 1.2.1, section 5.3.1,
//     https://tukaani.org/xz/xz-file-format.txt.
//   - Decision 0045: docs/research/decisions/0045-lzma2-growing-dictionary.md
//     (why the decoder is copied rather than taken from upstream).
//   - https://github.com/weaveplatform/artifactweave/issues/1413
//
// # Status
//
// Implemented after P9d (#1413). Copied from ulikunitz/xz v0.5.17, with
// these changes:
//
//   - buffer.go: data starts with startCap (64 KiB) or less and grows
//     (reserve, resize), calling grow first; Available counts against the
//     full size; newBuffer takes grow and returns its error; Write and
//     WriteByte return grow's error having written nothing; Discard,
//     Reset, matchLen and prefixLen, which only the encoder calls, are
//     left out.
//   - decoderdict.go: newDecoderDict takes grow; writeMatch reserves the
//     room for a match before it copies it.
//   - reader2.go: Reader2Config.Grow; MinDictCap and MaxDictCap, from
//     lzma/writer.go; no debug log (internal/xlog); NewReader2 without a
//     config and Reader2.EOS, which xzfile does not call, are left out.
//   - header2.go: only what reads a chunk header. MarshalBinary, the
//     String methods, defaultChunkType and the dictionary capacity codes
//     are left out.
//   - state.go, rangecodec.go, prob.go, treecodecs.go, directcodec.go,
//     literalcodec.go, lengthcodec.go, distcodec.go, properties.go and
//     operation.go: the decoder's halves. The encoders, deepcopy,
//     cloneState, String, Bits, verify, Code and maxDistance are left out.
//   - Not copied: the encoder, the classic LZMA Reader and Writer, Writer2,
//     header.go and bitops.go.
//   - Throughout, for the linters: an errors.New inside a function is a
//     package variable with the same text; a panic that formatted an error
//     with %s wraps it with %w or formats the same text; and lint
//     directives mark upstream's own comparisons with io.EOF, unwrapped
//     errors of the input and integer conversions, which are kept as they
//     are so no error reads differently.
//
// # Testing
//
//   - Upstream's tests of the decoder side, under the same licence, renamed
//     TestUnit*: buffer_test.go (TestBuffer_Write,
//     TestBuffer_Buffered_Available and TestBuffer_Read, reading where
//     they discarded), decoder_test.go (TestDecoder on fox.lzma, and
//     reader_test.go's example files through the decoder),
//     decoderdict_test.go, header2_test.go (with the chunk header encoder
//     in the test) and writer2_test.go's round trips through Reader2
//     (reader2_test.go).
//   - grow_test.go: a buffer that grows answers every write, read and room
//     check as one of its full size does, through its growth and its
//     wraps; Grow's capacities and its refusal at the start, in a
//     compressed chunk and in an uncompressed one; matches across the
//     growth; and Reader2 against ulikunitz/xz's, read by read, by content
//     and error text, for several streams, each also cut short and with a
//     byte changed at 24 places along it.
//   - edges_test.go: upstream's checks of what only a broken caller or
//     input gives (a bad or missing range coder start, a reader that stops
//     giving bytes, properties out of range, matches of no length or past
//     the room), and a write that Grow refuses leaving the dictionary as
//     it was.
//   - bench_test.go: BenchmarkReader2 decodes 64 MiB with a 64 MiB
//     dictionary with this Reader2 and with ulikunitz/xz's, so the cost of
//     the growth shows beside the allocation it saves.
//   - testdata holds upstream's fox.lzma and lzma/examples, the LZMA SDK's
//     test files, unchanged.
package lzma2

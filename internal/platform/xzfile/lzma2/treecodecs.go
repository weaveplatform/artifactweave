// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/treecodecs.go: the
// decoders, as doc.go lists.

package lzma2

// treeCodec encodes or decodes values with a fixed bit size. It is using a
// tree of probability value. The root of the tree is the most-significant bit.
type treeCodec struct {
	probTree
}

// makeTreeCodec makes a tree codec. The bits value must be inside the range
// [1,32].
func makeTreeCodec(bits int) treeCodec {
	return treeCodec{makeProbTree(bits)}
}

// Decode uses the range decoder to decode a fixed-bit-size value. Errors may
// be caused by the range decoder.
func (tc *treeCodec) Decode(d *rangeDecoder) (v uint32, err error) {
	m := uint32(1)
	for j := 0; j < int(tc.bits); j++ {
		b, err := d.DecodeBit(&tc.probs[m])
		if err != nil {
			return 0, err
		}
		m = (m << 1) | b
	}
	return m - (1 << uint(tc.bits)), nil
}

// treeReverseCodec is another tree codec, where the least-significant bit is
// the start of the probability tree.
type treeReverseCodec struct {
	probTree
}

// makeTreeReverseCodec creates treeReverseCodec value. The bits argument must
// be in the range [1,32].
func makeTreeReverseCodec(bits int) treeReverseCodec {
	return treeReverseCodec{makeProbTree(bits)}
}

// Decode uses the range decoder to decode a fixed-bit-size value. Errors
// returned by the range decoder will be returned.
func (tc *treeReverseCodec) Decode(d *rangeDecoder) (v uint32, err error) {
	m := uint32(1)
	for j := uint(0); j < uint(tc.bits); j++ {
		b, err := d.DecodeBit(&tc.probs[m])
		if err != nil {
			return 0, err
		}
		m = (m << 1) | b
		v |= b << j
	}
	return v, nil
}

// probTree stores enough probability values to be used by the treeEncode and
// treeDecode methods of the range coder types.
type probTree struct {
	probs []prob
	bits  byte
}

// makeProbTree initializes a probTree structure.
func makeProbTree(bits int) probTree {
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	if !(1 <= bits && bits <= 32) {
		panic("bits outside of range [1,32]")
	}
	t := probTree{
		bits:  byte(bits),
		probs: make([]prob, 1<<uint(bits)),
	}
	for i := range t.probs {
		t.probs[i] = probInit
	}
	return t
}

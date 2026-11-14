// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/lengthcodec.go: the
// decoder, as doc.go lists.

package lzma2

// maxPosBits defines the number of bits of the position value that are used to
// to compute the posState value. The value is used to select the tree codec
// for length encoding and decoding.
const maxPosBits = 4

// minMatchLen and maxMatchLen give the minimum and maximum values for
// encoding and decoding length values. minMatchLen is also used as base
// for the encoded length values.
const (
	minMatchLen = 2
	maxMatchLen = minMatchLen + 16 + 256 - 1
)

// lengthCodec support the encoding of the length value.
type lengthCodec struct {
	choice [2]prob
	low    [1 << maxPosBits]treeCodec
	mid    [1 << maxPosBits]treeCodec
	high   treeCodec
}

// init initializes a new length codec.
func (lc *lengthCodec) init() {
	for i := range lc.choice {
		lc.choice[i] = probInit
	}
	for i := range lc.low {
		lc.low[i] = makeTreeCodec(3)
	}
	for i := range lc.mid {
		lc.mid[i] = makeTreeCodec(3)
	}
	lc.high = makeTreeCodec(8)
}

// Decode reads the length offset. Add minMatchLen to compute the actual length
// to the length offset l.
func (lc *lengthCodec) Decode(d *rangeDecoder, posState uint32,
) (l uint32, err error) {
	var b uint32
	if b, err = lc.choice[0].Decode(d); err != nil {
		return
	}
	if b == 0 {
		l, err = lc.low[posState].Decode(d)
		return
	}
	if b, err = lc.choice[1].Decode(d); err != nil {
		return
	}
	if b == 0 {
		l, err = lc.mid[posState].Decode(d)
		l += 8
		return
	}
	l, err = lc.high.Decode(d)
	l += 16
	return
}

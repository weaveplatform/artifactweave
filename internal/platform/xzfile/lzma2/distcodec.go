// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/distcodec.go: the
// decoder, as doc.go lists.

package lzma2

// Constants used by the distance codec.
const (
	// minimum supported distance
	minDistance = 1
	// number of the supported len states
	lenStates = 4
	// start for the position models
	startPosModel = 4
	// first index with align bits support
	endPosModel = 14
	// bits for the position slots
	posSlotBits = 6
	// number of align bits
	alignBits = 4
)

// distCodec provides encoding and decoding of distance values.
type distCodec struct {
	posSlotCodecs [lenStates]treeCodec
	posModel      [endPosModel - startPosModel]treeReverseCodec
	alignCodec    treeReverseCodec
}

// init initializes a distance codec.
func (dc *distCodec) init() {
	for i := range dc.posSlotCodecs {
		dc.posSlotCodecs[i] = makeTreeCodec(posSlotBits)
	}
	for i := range dc.posModel {
		posSlot := startPosModel + i
		bits := (posSlot >> 1) - 1
		dc.posModel[i] = makeTreeReverseCodec(bits)
	}
	dc.alignCodec = makeTreeReverseCodec(alignBits)
}

// lenState converts the value l to a supported lenState value.
func lenState(l uint32) uint32 {
	if l >= lenStates {
		l = lenStates - 1
	}
	return l
}

// Decode decodes the distance offset using the parameter l. The dist value
// 0xffffffff (eos) indicates the end of the stream. Add one to the distance
// offset to get the actual match distance.
func (dc *distCodec) Decode(d *rangeDecoder, l uint32) (dist uint32, err error) {
	posSlot, err := dc.posSlotCodecs[lenState(l)].Decode(d)
	if err != nil {
		return
	}

	// posSlot equals distance
	if posSlot < startPosModel {
		return posSlot, nil
	}

	// posSlot uses the individual models
	bits := (posSlot >> 1) - 1
	dist = (2 | (posSlot & 1)) << bits
	var u uint32
	if posSlot < endPosModel {
		tc := &dc.posModel[posSlot-startPosModel]
		if u, err = tc.Decode(d); err != nil {
			return 0, err
		}
		dist += u
		return dist, nil
	}

	// posSlots use direct encoding and a single model for the four align
	// bits.
	//nolint:gosec // G115: bits is 1 to 30 for a 6-bit posSlot past endPosModel
	dic := directCodec(bits - alignBits)
	if u, err = dic.Decode(d); err != nil {
		return 0, err
	}
	dist += u << alignBits
	if u, err = dc.alignCodec.Decode(d); err != nil {
		return 0, err
	}
	dist += u
	return dist, nil
}

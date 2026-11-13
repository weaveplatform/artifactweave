// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/rangecodec.go: the
// range decoder, as doc.go lists.

package lzma2

import (
	"errors"
	"io"
)

// The range decoder's errors, as upstream words them.
var (
	errFirstByte = errors.New("newRangeDecoder: first byte not zero")
	errCodeRange = errors.New("newRangeDecoder: d.code >= d.nrange")
)

// rangeDecoder decodes single bits of the range encoding stream.
type rangeDecoder struct {
	br     io.ByteReader
	nrange uint32
	code   uint32
}

// newRangeDecoder initializes a range decoder. It reads five bytes from the
// reader and therefore may return an error.
func newRangeDecoder(br io.ByteReader) (d *rangeDecoder, err error) {
	d = &rangeDecoder{br: br, nrange: 0xffffffff}

	b, err := d.br.ReadByte()
	if err != nil {
		return nil, err //nolint:wrapcheck // the input's error: the decoder tells io.EOF apart
	}
	if b != 0 {
		return nil, errFirstByte
	}

	for i := 0; i < 4; i++ {
		if err = d.updateCode(); err != nil {
			return nil, err
		}
	}

	if d.code >= d.nrange {
		return nil, errCodeRange
	}

	return d, nil
}

// possiblyAtEnd checks whether the decoder may be at the end of the stream.
func (d *rangeDecoder) possiblyAtEnd() bool {
	return d.code == 0
}

// DirectDecodeBit decodes a bit with probability 1/2. The return value b will
// contain the bit at the least-significant position. All other bits will be
// zero.
func (d *rangeDecoder) DirectDecodeBit() (b uint32, err error) {
	d.nrange >>= 1
	d.code -= d.nrange
	t := 0 - (d.code >> 31)
	d.code += d.nrange & t
	b = (t + 1) & 1

	// d.code will stay less then d.nrange

	// normalize
	// assume d.code < d.nrange
	const top = 1 << 24
	if d.nrange >= top {
		return b, nil
	}
	d.nrange <<= 8
	// d.code < d.nrange will be maintained
	return b, d.updateCode()
}

// DecodeBit decodes a single bit. The bit will be returned at the
// least-significant position. All other bits will be zero. The probability
// value will be updated.
func (d *rangeDecoder) DecodeBit(p *prob) (b uint32, err error) {
	bound := p.bound(d.nrange)
	if d.code < bound {
		d.nrange = bound
		p.inc()
		b = 0
	} else {
		d.code -= bound
		d.nrange -= bound
		p.dec()
		b = 1
	}
	// normalize
	// assume d.code < d.nrange
	const top = 1 << 24
	if d.nrange >= top {
		return b, nil
	}
	d.nrange <<= 8
	// d.code < d.nrange will be maintained
	return b, d.updateCode()
}

// updateCode reads a new byte into the code.
func (d *rangeDecoder) updateCode() error {
	b, err := d.br.ReadByte()
	if err != nil {
		return err //nolint:wrapcheck // the input's error: the decoder tells io.EOF apart
	}
	d.code = (d.code << 8) | uint32(b)
	return nil
}

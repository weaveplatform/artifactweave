// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/directcodec.go: the
// decoder, as doc.go lists.

package lzma2

// directCodec allows the encoding and decoding of values with a fixed number
// of bits. The number of bits must be in the range [1,32].
type directCodec byte

// Decode uses the range decoder to decode a value with the given number of
// given bits. The most-significant bit is decoded first.
func (dc directCodec) Decode(d *rangeDecoder) (v uint32, err error) {
	for i := int(dc) - 1; i >= 0; i-- {
		x, err := d.DirectDecodeBit()
		if err != nil {
			return 0, err
		}
		v = (v << 1) | x
	}
	return v, nil
}

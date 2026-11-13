// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/literalcodec.go: the
// decoder, as doc.go lists.

package lzma2

// literalCodec supports the encoding of literal. It provides 768 probability
// values per literal state. The upper 512 probabilities are used with the
// context of a match bit.
type literalCodec struct {
	probs []prob
}

// init initializes the literal codec.
func (c *literalCodec) init(lc, lp int) {
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	switch {
	case !(minLC <= lc && lc <= maxLC):
		panic("lc out of range")
	case !(minLP <= lp && lp <= maxLP):
		panic("lp out of range")
	}
	c.probs = make([]prob, 0x300<<uint(lc+lp))
	for i := range c.probs {
		c.probs[i] = probInit
	}
}

// Decode decodes a literal byte using the range decoder as well as the LZMA
// state, a match byte, and the literal state.
func (c *literalCodec) Decode(d *rangeDecoder,
	state uint32, match byte, litState uint32,
) (s byte, err error) {
	k := litState * 0x300
	probs := c.probs[k : k+0x300]
	symbol := uint32(1)
	if state >= 7 {
		m := uint32(match)
		for {
			matchBit := (m >> 7) & 1
			m <<= 1
			i := ((1 + matchBit) << 8) | symbol
			bit, err := d.DecodeBit(&probs[i])
			if err != nil {
				return 0, err
			}
			symbol = (symbol << 1) | bit
			if matchBit != bit {
				break
			}
			if symbol >= 0x100 {
				break
			}
		}
	}
	for symbol < 0x100 {
		bit, err := d.DecodeBit(&probs[symbol])
		if err != nil {
			return 0, err
		}
		symbol = (symbol << 1) | bit
	}
	//nolint:gosec // G115: symbol is 0x100 to 0x1ff here
	s = byte(symbol - 0x100)
	return s, nil
}

// minLC and maxLC define the range for LC values.
const (
	minLC = 0
	maxLC = 8
)

// minLC and maxLC define the range for LP values.
const (
	minLP = 0
	maxLP = 4
)

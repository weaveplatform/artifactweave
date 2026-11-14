// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/state.go: the parts
// the decoder uses, as doc.go lists.

package lzma2

// states defines the overall state count
const states = 12

// State maintains the full state of the operation encoding or decoding
// process.
type state struct {
	rep         [4]uint32
	isMatch     [states << maxPosBits]prob
	isRepG0Long [states << maxPosBits]prob
	isRep       [states]prob
	isRepG0     [states]prob
	isRepG1     [states]prob
	isRepG2     [states]prob
	litCodec    literalCodec
	lenCodec    lengthCodec
	repLenCodec lengthCodec
	distCodec   distCodec
	state       uint32
	posBitMask  uint32
	Properties  Properties
}

// initProbSlice initializes a slice of probabilities.
func initProbSlice(p []prob) {
	for i := range p {
		p[i] = probInit
	}
}

// Reset sets all state information to the original values.
func (s *state) Reset() {
	p := s.Properties
	*s = state{
		Properties: p,
		// dict:       s.dict,
		posBitMask: (uint32(1) << uint(p.PB)) - 1,
	}
	initProbSlice(s.isMatch[:])
	initProbSlice(s.isRep[:])
	initProbSlice(s.isRepG0[:])
	initProbSlice(s.isRepG1[:])
	initProbSlice(s.isRepG2[:])
	initProbSlice(s.isRepG0Long[:])
	s.litCodec.init(p.LC, p.LP)
	s.lenCodec.init()
	s.repLenCodec.init()
	s.distCodec.init()
}

// newState creates a new state from the give Properties.
func newState(p Properties) *state {
	s := &state{Properties: p}
	s.Reset()
	return s
}

// updateStateLiteral updates the state for a literal.
func (s *state) updateStateLiteral() {
	switch {
	case s.state < 4:
		s.state = 0
		return
	case s.state < 10:
		s.state -= 3
		return
	}
	s.state -= 6
}

// updateStateMatch updates the state for a match.
func (s *state) updateStateMatch() {
	if s.state < 7 {
		s.state = 7
	} else {
		s.state = 10
	}
}

// updateStateRep updates the state for a repetition.
func (s *state) updateStateRep() {
	if s.state < 7 {
		s.state = 8
	} else {
		s.state = 11
	}
}

// updateStateShortRep updates the state for a short repetition.
func (s *state) updateStateShortRep() {
	if s.state < 7 {
		s.state = 9
	} else {
		s.state = 11
	}
}

// states computes the states of the operation codec.
func (s *state) states(dictHead int64) (state1, state2, posState uint32) {
	state1 = s.state
	//nolint:gosec // G115: only the low bits are kept, as upstream keeps them
	posState = uint32(dictHead) & s.posBitMask
	state2 = (s.state << maxPosBits) | posState
	return
}

// litState computes the literal state.
func (s *state) litState(prev byte, dictHead int64) uint32 {
	lp, lc := uint(s.Properties.LP), uint(s.Properties.LC)
	//nolint:gosec // G115: only the low bits are kept, as upstream keeps them
	litState := ((uint32(dictHead) & ((1 << lp) - 1)) << lc) |
		(uint32(prev) >> (8 - lc))
	return litState
}

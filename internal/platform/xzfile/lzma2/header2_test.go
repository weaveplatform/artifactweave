// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/header2_test.go:
// renamed TestUnit*, with the chunk header encoder the tests need, which
// the package leaves out, and without TestChunkTypeString, whose String
// methods it leaves out too, as doc.go lists.

package lzma2

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// marshal is upstream's chunkHeader.MarshalBinary, without the
// properties check, which only the encoder needs.
func (h *chunkHeader) marshal() []byte {
	data := make([]byte, headerLen(h.ctype))
	data[0] = [...]byte{cEOS: hEOS, cUD: hUD, cU: hU, cL: hL, cLR: hLR, cLRN: hLRN, cLRND: hLRND}[h.ctype]
	if h.ctype == cEOS {
		return data
	}
	data[1], data[2] = byte(h.uncompressed>>8), byte(h.uncompressed)
	if h.ctype <= cU {
		return data
	}
	data[0] |= byte(h.uncompressed>>16) &^ hLRND
	data[3], data[4] = byte(h.compressed>>8), byte(h.compressed)
	if h.ctype <= cLR {
		return data
	}
	data[5] = byte((h.props.PB*5+h.props.LP)*9 + h.props.LC)
	return data
}

func TestUnitHeaderChunkType(t *testing.T) {
	tests := []struct {
		h byte
		c chunkType
	}{
		{h: 0, c: cEOS},
		{h: 1, c: cUD},
		{h: 2, c: cU},
		{h: 1<<7 | 0x1f, c: cL},
		{h: 1<<7 | 1<<5 | 0x1f, c: cLR},
		{h: 1<<7 | 1<<6 | 0x1f, c: cLRN},
		{h: 1<<7 | 1<<6 | 1<<5 | 0x1f, c: cLRND},
		{h: 1<<7 | 1<<6 | 1<<5, c: cLRND},
	}
	if _, err := headerChunkType(3); err == nil {
		t.Fatalf("headerChunkType(%d) got %v; want %v",
			3, err, errHeaderByte)
	}
	for _, tc := range tests {
		c, err := headerChunkType(tc.h)
		if err != nil {
			t.Fatalf("headerChunkType error %s", err)
		}
		if c != tc.c {
			t.Errorf("got %d; want %d", c, tc.c)
		}
	}
}

func TestUnitHeaderLen(t *testing.T) {
	tests := []struct {
		c chunkType
		n int
	}{
		{cEOS, 1},
		{cU, 3},
		{cUD, 3},
		{cL, 5},
		{cLR, 5},
		{cLRN, 6},
		{cLRND, 6},
	}
	for _, tc := range tests {
		n := headerLen(tc.c)
		if n != tc.n {
			t.Errorf("header length for %d %d; want %d",
				tc.c, n, tc.n)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("headerLen of an unknown chunk type did not panic")
		}
	}()
	headerLen(cLRND + 1)
}

func chunkHeaderSamples(t *testing.T) []chunkHeader {
	_ = t
	props := Properties{LC: 3, LP: 0, PB: 2}
	headers := make([]chunkHeader, 0, 12)
	for c := cEOS; c <= cLRND; c++ {
		var h chunkHeader
		h.ctype = c
		if c >= cUD {
			h.uncompressed = 0x0304
		}
		if c >= cL {
			h.compressed = 0x0201
		}
		if c >= cLRN {
			h.props = props
		}
		headers = append(headers, h)
	}
	return headers
}

func TestUnitChunkHeaderMarshalling(t *testing.T) {
	for _, h := range chunkHeaderSamples(t) {
		data := h.marshal()
		var g chunkHeader
		if err := g.UnmarshalBinary(data); err != nil {
			t.Fatalf("UnmarshalBinary error %s", err)
		}
		if g != h {
			t.Fatalf("got %v; want %v", g, h)
		}
	}
}

func TestUnitReadChunkHeader(t *testing.T) {
	for _, h := range chunkHeaderSamples(t) {
		data := h.marshal()
		r := bytes.NewReader(data)
		g, err := readChunkHeader(r)
		if err != nil {
			t.Fatalf("readChunkHeader for %v error %s", h, err)
		}
		if *g != h {
			t.Fatalf("got %v; want %v", g, h)
		}
	}
}

func TestUnitReadEOS(t *testing.T) {
	var b [1]byte
	r := bytes.NewReader(b[:])
	h, err := readChunkHeader(r)
	if err != nil {
		t.Fatalf("readChunkHeader error %s", err)
	}
	if h.ctype != cEOS {
		t.Errorf("ctype got %d; want %d", h.ctype, cEOS)
	}
	if h.compressed != 0 {
		t.Errorf("compressed got %d; want %d", h.compressed, 0)
	}
	if h.uncompressed != 0 {
		t.Errorf("uncompressed got %d; want %d", h.uncompressed, 0)
	}
	wantProps := Properties{}
	if h.props != wantProps {
		t.Errorf("props got %v; want %v", h.props, wantProps)
	}
}

// A chunk header that is empty, short, long, of an unknown type, with
// properties out of range or cut off is refused in upstream's words.
func TestUnitBrokenChunkHeaders(t *testing.T) {
	for _, c := range []struct {
		data []byte
		want error
	}{
		{nil, errNoHeaderData},
		{[]byte{hL, 0}, errIncompleteHeader},
		{[]byte{hEOS, 0}, errHeaderLength},
		{[]byte{3}, errHeaderByte},
		{[]byte{hLRND, 0, 0, 0, 0, maxPropertyCode + 1}, errPropertiesCode},
	} {
		var h chunkHeader
		if err := h.UnmarshalBinary(c.data); !errors.Is(err, c.want) {
			t.Errorf("% x: %v, want %v", c.data, err, c.want)
		}
	}
	for _, c := range []struct {
		data []byte
		want error
	}{
		{nil, io.EOF},
		{[]byte{3}, errHeaderByte},
		{[]byte{hLRND, 0, 0}, io.ErrUnexpectedEOF},
		{[]byte{hLRND, 0, 0, 0, 0, maxPropertyCode + 1}, errPropertiesCode},
	} {
		if _, err := readChunkHeader(bytes.NewReader(c.data)); !errors.Is(err, c.want) {
			t.Errorf("read % x: %v, want %v", c.data, err, c.want)
		}
	}
}

// Every chunk type is taken or refused in every state as upstream's
// state machine takes it.
func TestUnitChunkStates(t *testing.T) {
	allowed := map[chunkState]map[chunkType]chunkState{
		'S': {cEOS: 'T', cUD: 'R', cLRND: 'L'},
		'L': {cEOS: 'T', cUD: 'R', cU: 'U', cL: 'L', cLR: 'L', cLRN: 'L', cLRND: 'L'},
		'R': {cEOS: 'T', cUD: 'R', cU: 'R', cLRN: 'L', cLRND: 'L'},
		'U': {cEOS: 'T', cUD: 'R', cU: 'U', cL: 'L', cLR: 'L', cLRN: 'L', cLRND: 'L'},
		'T': {},
	}
	for from, next := range allowed {
		for c := cEOS; c <= cLRND+1; c++ {
			s := from
			err := s.next(c)
			want, ok := next[c]
			switch {
			case ok && (err != nil || s != want):
				t.Errorf("%c on %d: %c %v, want %c", from, c, s, err, want)
			case !ok && !errors.Is(err, errChunkType):
				t.Errorf("%c on %d: %v, want %v", from, c, err, errChunkType)
			}
		}
	}
	bad := chunkState('X')
	if err := bad.next(cEOS); !errors.Is(err, errState) {
		t.Errorf("an unknown state: %v, want %v", err, errState)
	}
}

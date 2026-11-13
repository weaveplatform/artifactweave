// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/header2.go: the
// parts that read a chunk header, as doc.go lists.

package lzma2

import (
	"errors"
	"fmt"
	"io"
)

// chunkType represents the type of an LZMA2 chunk. Note that this
// value is an internal representation and no actual encoding of a LZMA2
// chunk header.
type chunkType byte

// Possible values for the chunk type.
const (
	// end of stream
	cEOS chunkType = iota
	// uncompressed; reset dictionary
	cUD
	// uncompressed; no reset of dictionary
	cU
	// LZMA compressed; no reset
	cL
	// LZMA compressed; reset state
	cLR
	// LZMA compressed; reset state; new property value
	cLRN
	// LZMA compressed; reset state; new property value; reset dictionary
	cLRND
)

// Actual encodings for the chunk types in the value. Note that the high
// uncompressed size bits are stored in the header byte additionally.
const (
	hEOS  = 0
	hUD   = 1
	hU    = 2
	hL    = 1 << 7
	hLR   = 1<<7 | 1<<5
	hLRN  = 1<<7 | 1<<6
	hLRND = 1<<7 | 1<<6 | 1<<5
)

// errHeaderByte indicates an unsupported value for the chunk header
// byte. These bytes starts the variable-length chunk header.
var errHeaderByte = errors.New("lzma: unsupported chunk header byte")

// The chunk header's other errors, as upstream words them.
var (
	errNoHeaderData     = errors.New("no data")
	errIncompleteHeader = errors.New("incomplete data")
	errHeaderLength     = errors.New("invalid data length")
)

// headerChunkType converts the header byte into a chunk type. It
// ignores the uncompressed size bits in the chunk header byte.
func headerChunkType(h byte) (c chunkType, err error) {
	if h&hL == 0 {
		// no compression
		switch h {
		case hEOS:
			c = cEOS
		case hUD:
			c = cUD
		case hU:
			c = cU
		default:
			return 0, errHeaderByte
		}
		return
	}
	switch h & hLRND {
	case hL:
		c = cL
	case hLR:
		c = cLR
	case hLRN:
		c = cLRN
	case hLRND:
		c = cLRND
	default:
		return 0, errHeaderByte
	}
	return
}

// uncompressedHeaderLen provides the length of an uncompressed header
const uncompressedHeaderLen = 3

// headerLen returns the length of the LZMA2 header for a given chunk
// type.
func headerLen(c chunkType) int {
	switch c {
	case cEOS:
		return 1
	case cU, cUD:
		return uncompressedHeaderLen
	case cL, cLR:
		return 5
	case cLRN, cLRND:
		return 6
	}
	panic(fmt.Sprintf("unsupported chunk type %d", c))
}

// chunkHeader represents the contents of a chunk header.
type chunkHeader struct {
	ctype        chunkType
	uncompressed uint32
	compressed   uint16
	props        Properties
}

// UnmarshalBinary reads the content of the chunk header from the data
// slice. The slice must have the correct length.
func (h *chunkHeader) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		return errNoHeaderData
	}
	c, err := headerChunkType(data[0])
	if err != nil {
		return err
	}

	n := headerLen(c)
	if len(data) < n {
		return errIncompleteHeader
	}
	if len(data) > n {
		return errHeaderLength
	}

	*h = chunkHeader{ctype: c}
	if c == cEOS {
		return nil
	}

	h.uncompressed = uint32(uint16BE(data[1:3]))
	if c <= cU {
		return nil
	}
	h.uncompressed |= uint32(data[0]&^hLRND) << 16

	h.compressed = uint16BE(data[3:5])
	if c <= cLR {
		return nil
	}

	h.props, err = PropertiesForCode(data[5])
	return err
}

// readChunkHeader reads the chunk header from the IO reader.
func readChunkHeader(r io.Reader) (h *chunkHeader, err error) {
	p := make([]byte, 1, 6)
	if _, err = io.ReadFull(r, p); err != nil {
		return //nolint:wrapcheck // the input's error: the caller tells io.EOF apart
	}
	c, err := headerChunkType(p[0])
	if err != nil {
		return
	}
	p = p[:headerLen(c)]
	if _, err = io.ReadFull(r, p[1:]); err != nil {
		return //nolint:wrapcheck // the input's error, as upstream returns it
	}
	h = new(chunkHeader)
	if err = h.UnmarshalBinary(p); err != nil {
		return nil, err
	}
	return h, nil
}

// uint16BE converts a big-endian uint16 representation to an uint16
// value.
func uint16BE(p []byte) uint16 {
	return uint16(p[0])<<8 | uint16(p[1])
}

// chunkState is used to manage the state of the chunks
type chunkState byte

// start and stop define the initial and terminating state of the chunk
// state
const (
	start chunkState = 'S'
	stop  chunkState = 'T'
)

// errors for the chunk state handling
var (
	errChunkType = errors.New("lzma: unexpected chunk type")
	errState     = errors.New("lzma: wrong chunk state")
)

// next transitions state based on chunk type input
func (c *chunkState) next(ctype chunkType) error {
	switch *c {
	// start state
	case 'S':
		switch ctype {
		case cEOS:
			*c = 'T'
		case cUD:
			*c = 'R'
		case cLRND:
			*c = 'L'
		default:
			return errChunkType
		}
	// normal LZMA mode
	case 'L':
		switch ctype {
		case cEOS:
			*c = 'T'
		case cUD:
			*c = 'R'
		case cU:
			*c = 'U'
		case cL, cLR, cLRN, cLRND:
			break
		default:
			return errChunkType
		}
	// reset required
	case 'R':
		switch ctype {
		case cEOS:
			*c = 'T'
		case cUD, cU:
			break
		case cLRN, cLRND:
			*c = 'L'
		default:
			return errChunkType
		}
	// uncompressed
	case 'U':
		switch ctype {
		case cEOS:
			*c = 'T'
		case cUD:
			*c = 'R'
		case cU:
			break
		case cL, cLR, cLRN, cLRND:
			*c = 'L'
		default:
			return errChunkType
		}
	// terminal state
	case 'T':
		return errChunkType
	default:
		return errState
	}
	return nil
}

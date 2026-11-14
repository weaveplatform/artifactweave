// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/decoderdict.go, and
// changed as doc.go lists: the dictionary grows as it fills.

package lzma2

import (
	"errors"
	"fmt"
)

// The dictionary's errors, as upstream words them.
var (
	errDictCap       = errors.New("lzma: dictCap out of range")
	errDistanceRange = errors.New("writeMatch: distance out of range")
	errLengthRange   = errors.New("writeMatch: length out of range")
)

// decoderDict provides the dictionary for the decoder. The whole
// dictionary is used as reader buffer.
type decoderDict struct {
	buf  buffer
	head int64
}

// newDecoderDict creates a new decoder dictionary. The whole dictionary
// will be used as reader buffer. It allocates the dictionary as it fills,
// calling grow, if set, before each allocation.
func newDecoderDict(dictCap int, grow func(capacity int) error) (d *decoderDict, err error) {
	// lower limit supports easy test cases
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	if !(1 <= dictCap && int64(dictCap) <= MaxDictCap) {
		return nil, errDictCap
	}
	buf, err := newBuffer(dictCap, grow)
	if err != nil {
		return nil, err
	}
	d = &decoderDict{buf: *buf}
	return d, nil
}

// Reset clears the dictionary. The read buffer is not changed, so the
// buffered data can still be read.
func (d *decoderDict) Reset() {
	d.head = 0
}

// WriteByte writes a single byte into the dictionary. It is used to
// write literals into the dictionary.
func (d *decoderDict) WriteByte(c byte) error {
	if err := d.buf.WriteByte(c); err != nil {
		return err
	}
	d.head++
	return nil
}

// pos returns the position of the dictionary head.
func (d *decoderDict) pos() int64 { return d.head }

// dictLen returns the actual length of the dictionary.
//
// It counts against the whole capacity, not what the buffer has grown
// to: head never passes front before the buffer has grown in full, so a
// match within dictLen reads only bytes the buffer holds, and one past it
// is refused with the same text whatever the buffer has grown to.
func (d *decoderDict) dictLen() int {
	capacity := d.buf.Cap()
	if d.head >= int64(capacity) {
		return capacity
	}
	return int(d.head)
}

// byteAt returns a byte stored in the dictionary. If the distance is
// non-positive or exceeds the current length of the dictionary the zero
// byte is returned.
func (d *decoderDict) byteAt(dist int) byte {
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	if !(0 < dist && dist <= d.dictLen()) {
		return 0
	}
	i := d.buf.front - dist
	if i < 0 {
		i += len(d.buf.data)
	}
	return d.buf.data[i]
}

// writeMatch writes the match at the top of the dictionary. The given
// distance must point in the current dictionary and the length must not
// exceed the maximum length 273 supported in LZMA.
//
// The error value ErrNoSpace indicates that no space is available in
// the dictionary for writing. You need to read from the dictionary
// first.
func (d *decoderDict) writeMatch(dist int64, length int) error {
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	if !(0 < dist && dist <= int64(d.dictLen())) {
		return errDistanceRange
	}
	//nolint:staticcheck // QF1001: upstream's form, kept as v0.5.17 writes it
	if !(0 < length && length <= maxMatchLen) {
		return errLengthRange
	}
	if length > d.buf.Available() {
		return ErrNoSpace
	}
	// The buffer grows here, if it must, and not in the writes below: they
	// copy from data, and must copy from the slice they write to.
	if err := d.buf.reserve(length); err != nil {
		return err
	}
	d.head += int64(length)

	i := d.buf.front - int(dist)
	if i < 0 {
		i += len(d.buf.data)
	}
	for length > 0 {
		var p []byte
		if i >= d.buf.front {
			p = d.buf.data[i:]
			i = 0
		} else {
			p = d.buf.data[i:d.buf.front]
			i = d.buf.front
		}
		if len(p) > length {
			p = p[:length]
		}
		if _, err := d.buf.Write(p); err != nil {
			panic(fmt.Errorf("d.buf.Write returned error %w", err))
		}
		length -= len(p)
	}
	return nil
}

// Write writes the given bytes into the dictionary and advances the
// head.
func (d *decoderDict) Write(p []byte) (n int, err error) {
	n, err = d.buf.Write(p)
	d.head += int64(n)
	return n, err
}

// Available returns the number of available bytes for writing into the
// decoder dictionary.
func (d *decoderDict) Available() int { return d.buf.Available() }

// Read reads data from the buffer contained in the decoder dictionary.
func (d *decoderDict) Read(p []byte) (n int, err error) { return d.buf.Read(p) }

// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/buffer.go, and changed
// as doc.go lists: the buffer grows as it fills.

package lzma2

import (
	"errors"
)

// buffer provides a circular buffer of bytes. If the front index equals
// the rear index the buffer is empty. As a consequence front cannot be
// equal rear for a full buffer. So a full buffer has a length that is
// one byte less the the length of the data slice.
//
// data starts shorter than size and grows as the buffer fills (#1413).
// Every answer is the one a buffer that started with all of size gives:
// until data has grown to size, front has never wrapped, so the bytes the
// buffer holds are data[:front] at the indices they would have there, and
// Available counts against size, not len(data).
type buffer struct {
	data  []byte
	front int
	rear  int
	// size is the length data has once it has grown in full: the
	// capacity plus one.
	size int
	// grow, if set, is called with each capacity data is about to grow
	// to, before it is allocated; an error from it ends the write.
	grow func(capacity int) error
}

// startCap is the capacity a buffer starts with, or its whole capacity
// if that is smaller: the 64 KiB that #1413 proposes, so a file of a few
// bytes takes 64 KiB whatever its block header names. Starting smaller
// would save little: the most an LZMA2 chunk decodes to is 2 MiB.
const startCap = 64 << 10

// newBuffer creates a buffer with the given size, of which it allocates
// startCap or less until it fills.
func newBuffer(size int, grow func(capacity int) error) (*buffer, error) {
	b := &buffer{size: size + 1, grow: grow}
	if err := b.resize(min(size, startCap)); err != nil {
		return nil, err
	}
	return b, nil
}

// Cap returns the capacity of the buffer.
func (b *buffer) Cap() int {
	return b.size - 1
}

// Buffered returns the number of bytes buffered.
func (b *buffer) Buffered() int {
	delta := b.front - b.rear
	if delta < 0 {
		delta += len(b.data)
	}
	return delta
}

// Available returns the number of bytes available for writing. It counts
// against size, as the buffer that starts with all of it does, so the
// decoder decodes as much at a time before the buffer grows as after.
func (b *buffer) Available() int {
	delta := b.rear - 1 - b.front
	if delta < 0 {
		delta += b.size
	}
	return delta
}

// addIndex adds a non-negative integer to the index i and returns the
// resulting index. The function takes care of wrapping the index as
// well as potential overflow situations.
func (b *buffer) addIndex(i int, n int) int {
	// subtraction of len(b.data) prevents overflow
	i += n - len(b.data)
	if i < 0 {
		i += len(b.data)
	}
	return i
}

// reserve grows data, before n bytes are written at front, so that they
// end below len(data): until data has grown in full the write may not
// wrap, as it would not where data has all of size. It at least doubles
// the capacity, so the bytes copied over all the growth are fewer than
// the capacity reached. Once data has grown in full the buffer wraps as
// the one that starts with all of size does.
func (b *buffer) reserve(n int) error {
	end := b.front + n
	if len(b.data) == b.size || end < len(b.data) {
		return nil
	}
	return b.resize(min(max(2*(len(b.data)-1), end), b.size-1))
}

// resize replaces data with one of the given capacity that holds the
// bytes written so far at the same indices, once grow allows it.
func (b *buffer) resize(capacity int) error {
	if b.grow != nil {
		if err := b.grow(capacity); err != nil {
			return err
		}
	}
	data := make([]byte, capacity+1)
	copy(data, b.data[:b.front])
	b.data = data
	return nil
}

// Read reads bytes from the buffer into p and returns the number of
// bytes read. The function never returns an error but might return less
// data than requested.
func (b *buffer) Read(p []byte) (n int, err error) {
	n, err = b.Peek(p)
	b.rear = b.addIndex(b.rear, n)
	return n, err
}

// Peek reads bytes from the buffer into p without changing the buffer.
// Peek will never return an error but might return less data than
// requested.
func (b *buffer) Peek(p []byte) (n int, err error) {
	m := b.Buffered()
	n = len(p)
	if m < n {
		n = m
		p = p[:n]
	}
	k := copy(p, b.data[b.rear:])
	if k < n {
		copy(p[k:], b.data)
	}
	return n, nil
}

// ErrNoSpace indicates that there is insufficient space for the Write
// operation.
var ErrNoSpace = errors.New("insufficient space")

// Write puts data into the  buffer. If less bytes are written than
// requested ErrNoSpace is returned. An error from grow writes nothing.
func (b *buffer) Write(p []byte) (n int, err error) {
	m := b.Available()
	n = len(p)
	if m < n {
		n = m
		p = p[:m]
		err = ErrNoSpace
	}
	if gerr := b.reserve(n); gerr != nil {
		return 0, gerr
	}
	k := copy(b.data[b.front:], p)
	if k < n {
		copy(b.data, p[k:])
	}
	b.front = b.addIndex(b.front, n)
	return n, err
}

// WriteByte writes a single byte into the buffer. The error ErrNoSpace
// is returned if no single byte is available in the buffer for writing.
func (b *buffer) WriteByte(c byte) error {
	if b.Available() < 1 {
		return ErrNoSpace
	}
	if err := b.reserve(1); err != nil {
		return err
	}
	b.data[b.front] = c
	b.front = b.addIndex(b.front, 1)
	return nil
}

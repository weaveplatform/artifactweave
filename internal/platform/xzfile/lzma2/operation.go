// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/operation.go, without
// the String methods only upstream's debug log called, as doc.go lists.

package lzma2

// operation represents an operation on the dictionary during encoding or
// decoding.
type operation interface {
	Len() int
}

// rep represents a repetition at the given distance and the given length
type match struct {
	// supports all possible distance values, including the eos marker
	distance int64
	// length
	n int
}

// Len returns the number of bytes matched.
func (m match) Len() int {
	return m.n
}

// lit represents a single byte literal.
type lit struct {
	b byte
}

// Len returns 1 for the single byte literal.
func (l lit) Len() int {
	return 1
}

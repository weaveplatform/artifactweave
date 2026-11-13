// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/breader.go, as it is
// but for a lint directive.

package lzma2

import (
	"io"
)

// breader provides the ReadByte function for a Reader. It doesn't read
// more data from the reader than absolutely necessary.
type breader struct {
	io.Reader
	// helper slice to save allocations
	p []byte
}

// ByteReader converts an io.Reader into an io.ByteReader.
func ByteReader(r io.Reader) io.ByteReader {
	br, ok := r.(io.ByteReader)
	if !ok {
		return &breader{r, make([]byte, 1)}
	}
	return br
}

// ReadByte read byte function.
func (r *breader) ReadByte() (c byte, err error) {
	for i := 0; i < 100; i++ {
		//nolint:staticcheck // QF1008: upstream's form, kept as v0.5.17 writes it
		n, err := r.Reader.Read(r.p)
		if n > 0 {
			return r.p[0], nil
		}
		if err != nil {
			return 0, err //nolint:wrapcheck // the input's error: the decoder tells io.EOF apart
		}
	}
	return 0, io.ErrNoProgress
}

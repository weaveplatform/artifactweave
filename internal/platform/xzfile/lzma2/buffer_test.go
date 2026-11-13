// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/buffer_test.go: the
// tests of what the decoder uses, renamed TestUnit*, each Discard a read,
// as doc.go lists.

package lzma2

import (
	"bytes"
	"testing"
)

// mustBuffer is newBuffer without grow, which cannot fail.
func mustBuffer(t *testing.T, size int) *buffer {
	t.Helper()
	buf, err := newBuffer(size, nil)
	if err != nil {
		t.Fatalf("newBuffer(%d) error %s", size, err)
	}
	return buf
}

// discard reads n bytes from buf and drops them, as upstream's Discard
// did, which the decoder never calls.
func discard(buf *buffer, n int) (int, error) {
	return buf.Read(make([]byte, n))
}

func TestUnitBufferWrite(t *testing.T) {
	buf := mustBuffer(t, 10)
	b := []byte("1234567890")
	for i := range b {
		n, err := buf.Write(b[i : i+1])
		if err != nil {
			t.Fatalf("buf.Write(b[%d:%d]) error %s", i, i+1, err)
		}
		if n != 1 {
			t.Fatalf("buf.Write(b[%d:%d]) returned %d; want %d",
				i, i+1, n, 1)
		}
	}
	const c = 8
	n, err := discard(buf, c)
	if err != nil {
		t.Fatalf("Discard error %s", err)
	}
	if n != c {
		t.Fatalf("Discard returned %d; want %d", n, c)
	}
	n, err = buf.Write(b)
	if err == nil {
		t.Fatalf("Write length exceed returned no error; n %d", n)
	}
	if n != c {
		t.Fatalf("Write length exceeding returned %d; want %d", n, c)
	}
	n, err = discard(buf, 4)
	if err != nil {
		t.Fatalf("Discard error %s", err)
	}
	if n != 4 {
		t.Fatalf("Discard returned %d; want %d", n, 4)
	}
	n, err = buf.Write(b[:3])
	if err != nil {
		t.Fatalf("buf.Write(b[:3]) error %s; n %d", err, n)
	}
	if n != 3 {
		t.Fatalf("buf.Write(b[:3]) returned %d; want %d", n, 3)
	}
}

func TestUnitBufferBufferedAvailable(t *testing.T) {
	buf := mustBuffer(t, 19)
	b := []byte("0123456789")
	var err error
	if _, err = buf.Write(b); err != nil {
		t.Fatalf("buf.Write(b) error %s", err)
	}
	if n := buf.Buffered(); n != 10 {
		t.Fatalf("buf.Buffered() returns %d; want %d", n, 10)
	}
	if _, err = discard(buf, 8); err != nil {
		t.Fatalf("buf.Discard(8) error %s", err)
	}
	if _, err = buf.Write(b[:7]); err != nil {
		t.Fatalf("buf.Write(b[:7]) error %s", err)
	}
	if n := buf.Buffered(); n != 9 {
		t.Fatalf("buf.Buffered() returns %d; want %d", n, 9)
	}
}

func TestUnitBufferRead(t *testing.T) {
	buf := mustBuffer(t, 10)
	b := []byte("0123456789")
	var err error
	if _, err = buf.Write(b); err != nil {
		t.Fatalf("buf.Write(b) error %s", err)
	}
	p := make([]byte, 8)
	n, err := buf.Read(p)
	if err != nil {
		t.Fatalf("buf.Read(p) error %s", err)
	}
	if n != len(p) {
		t.Fatalf("buf.Read(p) returned %d; want %d", n, len(p))
	}
	if !bytes.Equal(p, b[:8]) {
		t.Fatalf("buf.Read(p) put %s into p; want %s", p, b[:8])
	}
	if _, err = buf.Write(b[:7]); err != nil {
		t.Fatalf("buf.Write(b[:7]) error %s", err)
	}
	q := make([]byte, 7)
	n, err = buf.Read(q)
	if err != nil {
		t.Fatalf("buf.Read(q) error %s", err)
	}
	if n != len(q) {
		t.Fatalf("buf.Read(q) returns %d; want %d", n, len(q))
	}
	c := []byte("8901234")
	if !bytes.Equal(q, c) {
		t.Fatalf("buf.Read(q) put %s into q; want %s", q, c)
	}
	if _, err := buf.Write(b[7:]); err != nil {
		t.Fatalf("buf.Write(b[7:]) error %s", err)
	}
	if _, err := buf.Write(b[:2]); err != nil {
		t.Fatalf("buf.Write(b[:2]) error %s", err)
	}
	t.Logf("buf.rear %d buf.front %d", buf.rear, buf.front)
	r := make([]byte, 2)
	n, err = buf.Read(r)
	if err != nil {
		t.Fatalf("buf.Read(r) error %s", err)
	}
	if n != len(r) {
		t.Fatalf("buf.Read(r) returns %d; want %d", n, len(r))
	}
	d := []byte("56")
	if !bytes.Equal(r, d) {
		t.Fatalf("buf.Read(r) put %s into r; want %s", r, d)
	}
}

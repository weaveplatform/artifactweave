// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/writer2_test.go: the
// round trips through Reader2, renamed TestUnit*, with upstream's Writer2,
// which this package leaves out, writing the data, and words from
// math/rand standing in for upstream's internal randtxt, as doc.go lists.

package lzma2

import (
	"bytes"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

func TestUnitCycle1(t *testing.T) {
	var buf bytes.Buffer
	w, err := lzma.Writer2Config{DictCap: 4096}.NewWriter2(&buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	n, err := w.Write([]byte{'a'})
	if err != nil {
		t.Fatalf("w.Write([]byte{'a'}) error %s", err)
	}
	if n != 1 {
		t.Fatalf("w.Write([]byte{'a'}) returned %d; want %d", n, 1)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close() error %s", err)
	}
	r, err := Reader2Config{DictCap: 4096}.NewReader2(&buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	p := make([]byte, 3)
	n, err = r.Read(p)
	t.Logf("n %d error %v", n, err)
	if n != 1 || p[0] != 'a' {
		t.Fatalf("read %q", p[:n])
	}
}

// text is n bytes of words drawn from a small vocabulary, compressible as
// prose is, the same for the same seed.
func text(seed uint64, n int) []byte {
	words := strings.Fields("the quick brown fox jumps over lazy dog and " +
		"a package index of names versions checksums sizes paths repository")
	rng := rand.New(rand.NewPCG(seed, seed))
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(words[rng.IntN(len(words))])
		b.WriteByte(" \n"[rng.IntN(2)])
	}
	return b.Bytes()[:n]
}

func TestUnitCycle2(t *testing.T) {
	buf := new(bytes.Buffer)
	w, err := lzma.Writer2Config{DictCap: 4096}.NewWriter2(buf)
	if err != nil {
		t.Fatalf("NewWriter error %s", err)
	}
	// const txtlen = 1024
	const txtlen = 2100000
	txt := string(text(42, txtlen))
	n, err := io.Copy(w, strings.NewReader(txt))
	if err != nil {
		t.Fatalf("Compressing copy error %s", err)
	}
	if n != txtlen {
		t.Fatalf("Compressing data length %d; want %d", n, txtlen)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("w.Close error %s", err)
	}
	t.Logf("buf.Len() %d", buf.Len())
	r, err := Reader2Config{DictCap: 4096}.NewReader2(buf)
	if err != nil {
		t.Fatalf("NewReader error %s", err)
	}
	out := new(bytes.Buffer)
	n, err = io.Copy(out, r)
	if err != nil {
		t.Fatalf("Decompressing copy error %s after %d bytes", err, n)
	}
	if n != txtlen {
		t.Fatalf("Decompression data length %d; want %d", n, txtlen)
	}
	if txt != out.String() {
		t.Fatal("decompressed data differs from original")
	}
}

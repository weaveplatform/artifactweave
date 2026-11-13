// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/decoder_test.go, and
// lzma/reader_test.go's TestReaderAll and TestReaderBadFiles, run through
// the decoder, which the classic Reader they tested wraps, as doc.go lists.

package lzma2

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitDecoder(t *testing.T) {
	filename := "testdata/fox.lzma"
	want := "The quick brown fox jumps over the lazy dog.\n"
	for i := 0; i < 2; i++ {
		f, err := os.Open(filename)
		if err != nil {
			t.Fatalf("os.Open(%q) error %s", filename, err)
		}
		p := make([]byte, 13)
		_, err = io.ReadFull(f, p)
		if err != nil {
			t.Fatalf("io.ReadFull error %s", err)
		}
		props, err := PropertiesForCode(p[0])
		if err != nil {
			t.Fatalf("p[0] error %s", err)
		}
		state := newState(props)
		const capacity = 0x800000
		dict, err := newDecoderDict(capacity, nil)
		if err != nil {
			t.Fatalf("newDecoderDict: error %s", err)
		}
		size := int64(-1)
		if i > 0 {
			size = int64(len(want))
		}
		br := bufio.NewReader(f)
		r, err := newDecoder(br, state, dict, size)
		if err != nil {
			t.Fatalf("newDecoder error %s", err)
		}
		bytes, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("[%d] ReadAll error %s", i, err)
		}
		if err = f.Close(); err != nil {
			t.Fatalf("Close error %s", err)
		}
		got := string(bytes)
		if got != want {
			t.Fatalf("read %q; but want %q", got, want)
		}
	}
}

// decodeClassic decodes a classic .lzma file with the decoder: the
// properties, dictionary size and content size of its 13-byte header, as
// upstream's Reader reads them, then the raw stream.
func decodeClassic(t *testing.T, data []byte) ([]byte, error) {
	t.Helper()
	props, err := PropertiesForCode(data[0])
	if err != nil {
		return nil, err
	}
	dictCap := max(int(binary.LittleEndian.Uint32(data[1:5])), MinDictCap)
	size := int64(
		binary.LittleEndian.Uint64(data[5:13]),
	) //nolint:gosec // G115: -1 is the unknown size
	dict, err := newDecoderDict(dictCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDecoder(bytes.NewReader(data[13:]), newState(props), dict, size)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(d)
}

// The LZMA SDK's example files decode to a.txt, each with its own
// properties, end marker and declared size, and the broken ones are
// refused.
func TestUnitDecoderExamples(t *testing.T) {
	orig, err := os.ReadFile("testdata/examples/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("testdata/examples/*.lzma")
	if err != nil || len(files) != 7 {
		t.Fatalf("examples: %v %v", files, err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeClassic(t, data)
		if strings.HasPrefix(filepath.Base(name), "bad") {
			if err == nil {
				t.Errorf("%s decoded with no error", name)
			}
			continue
		}
		if err != nil || !bytes.Equal(got, orig) {
			t.Errorf("%s: %d bytes, %v", name, len(got), err)
		}
	}
}

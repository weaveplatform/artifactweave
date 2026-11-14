// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/decoderdict_test.go,
// renamed TestUnit*, as doc.go lists.

package lzma2

import (
	"testing"
)

func TestUnitNewDecoderDict(t *testing.T) {
	if _, err := newDecoderDict(0, nil); err == nil {
		t.Fatalf("no error for zero dictionary capacity")
	}
	if _, err := newDecoderDict(8, nil); err != nil {
		t.Fatalf("error %s", err)
	}
}

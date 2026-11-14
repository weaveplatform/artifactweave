// Copyright 2014-2022 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// Copied from github.com/ulikunitz/xz v0.5.17, lzma/properties.go: the
// parts the decoder uses, as doc.go lists.

package lzma2

import (
	"errors"
)

// maximum value for the LZMA properties.
const maxPB = 4

// maxPropertyCode is the possible maximum of a properties code byte.
const maxPropertyCode = (maxPB+1)*(maxLP+1)*(maxLC+1) - 1

// errPropertiesCode is upstream's error for a properties byte above
// maxPropertyCode.
var errPropertiesCode = errors.New("lzma: invalid properties code")

// Properties contains the parameters LC, LP and PB. The parameter LC
// defines the number of literal context bits; parameter LP the number
// of literal position bits and PB the number of position bits.
type Properties struct {
	LC int
	LP int
	PB int
}

// PropertiesForCode converts a properties code byte into a Properties value.
func PropertiesForCode(code byte) (p Properties, err error) {
	if code > maxPropertyCode {
		return p, errPropertiesCode
	}
	p.LC = int(code % 9)
	code /= 9
	p.LP = int(code % 5)
	code /= 5
	p.PB = int(code % 5)
	return p, err
}

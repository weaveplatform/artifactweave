package npm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"strings"
	"testing"
	"testing/iotest"
)

func TestUnitIntegrityStrongestAlgorithmAndAlternatives(t *testing.T) {
	body := []byte("verified package")
	good256, good384, good512 := sha256.Sum256(body), sha512.Sum384(body), sha512.Sum512(body)
	wrong512 := sha512.Sum512([]byte("different package"))
	token := func(name string, digest []byte) string { return name + "-" + base64.StdEncoding.EncodeToString(digest) }
	for _, tc := range []struct {
		raw       string
		good      bool
		algorithm string
	}{
		{token("sha384", good384[:]), true, "sha384"},
		{token("sha512", wrong512[:]) + " " + token("sha512", good512[:]), true, "sha512"},
		{token("sha256", good256[:]) + " " + token("sha512", wrong512[:]), false, "sha512"},
		{token("sha384", good384[:]) + " " + token("sha256", good256[:]), true, "sha384"},
		{"sha256-" + base64.RawStdEncoding.EncodeToString(good256[:]) + "?option", true, "sha256"},
	} {
		spec, err := parseIntegrity(tc.raw)
		if err != nil || spec.algorithm != tc.algorithm {
			t.Fatalf("parse %+v %v", spec, err)
		}
		if err = spec.validate(
			context.Background(),
			bytes.NewReader(body),
			int64(len(body)),
		); (err == nil) != tc.good {
			t.Fatalf("verify %s: %v", tc.raw, err)
		}
		if err = spec.verify(iotest.ErrReader(remoteProtocolError())); err == nil {
			t.Fatal("read error ignored")
		}
	}
	if _, err := parseIntegrity("unknown-AAAA"); err == nil {
		t.Fatal("unknown-only integrity accepted")
	}
	if _, err := parseIntegrity("sha384-AAAA"); err == nil {
		t.Fatal("truncated SHA384 accepted")
	}
}

func TestUnitIntegrityChecksumPinsUseStrongestOnly(t *testing.T) {
	strong := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64))
	alternative := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 64))
	weak := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	sums, err := distChecksums("sha512-"+strong+" sha256-"+weak, "")
	if err != nil || sums.SHA512 == "" || sums.SHA256 != "" {
		t.Fatalf("pins %+v %v", sums, err)
	}
	sums, err = distChecksums("sha512-"+strong+" sha512-"+alternative, strings.Repeat("a", 40))
	if err != nil || sums.SHA512 != "" || sums.SHA1 == "" {
		t.Fatalf("alternatives %+v %v", sums, err)
	}
}

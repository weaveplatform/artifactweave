package huggingface

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/crypt"
)

func TestUnitXetAuthenticationBypassIsLimitedToCASRoutes(t *testing.T) {
	p := Protocol(Options{})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"xet/models/acme/demo/main/v1/shards", true},
		{"acme/demo/resolve/main/xet/weights.bin", false},
		{"api/models/xet/demo", false},
		{"lfs/upload/digest?sig=test", true},
		{"acme/demo/resolve/main/lfs/upload/digest?sig=test", false},
	} {
		r := httptest.NewRequest("PUT", "/artifactory/api/huggingfaceml/hf/"+tc.path, nil)
		if p.SkipsAuthentication(r) != tc.want {
			t.Errorf("authentication bypass for %s", tc.path)
		}
	}
}

func TestUnitXetCapabilitySupportsMaximumEscapedClaimFields(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte("x"), 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	h := handler{o: Options{Keys: keys, Now: func() time.Time { return now }}}
	claim := xetClaim{
		Repo:      strings.Repeat("\x01", 64),
		Member:    strings.Repeat("\x01", 64),
		Kind:      "datasets",
		Owner:     strings.Repeat("a", 96),
		Name:      strings.Repeat("a", 96),
		Reference: strings.Repeat("a", 128),
		Revision:  strings.Repeat("a", 128),
		File: strings.Join(
			[]string{
				strings.Repeat("<", 255),
				strings.Repeat("<", 255),
				strings.Repeat("<", 255),
				strings.Repeat("<", 254),
				"x",
			},
			"/",
		),
		Subject: strings.Repeat("<", 255),
		Issued:  now.Unix(),
		Expires: now.Add(xetTTL).Unix(),
	}
	var revision [32]byte
	for i := range revision {
		revision[i] = 255
	}
	for _, credential := range []auth.DownloadCredential{
		{Kind: "password", PasswordRevision: revision},
		{Kind: "token", TokenID: "11111111-1111-4111-8111-111111111111"},
	} {
		claim.Credential = credential
		token := h.signXet(claim)
		if got, ok := h.verifyXet(token); !ok || got != claim {
			t.Fatalf("maximum supported claim (%d encoded bytes) did not round trip", len(token))
		}
	}
}

func TestUnitXetRangeAndAuthorizedChunkBounds(t *testing.T) {
	for _, tc := range []struct {
		value            string
		size, start, end int64
		ok               bool
	}{
		{"", 100, 0, 99, true},
		{"bytes=10-20", 100, 10, 20, true},
		{"bytes=10-", 100, 10, 99, true},
		{"bytes=0-18446744073709551615", 100, 0, 99, true},
		{"bytes=0-1000", 100, 0, 99, true},
		{"", 0, 0, 0, false},
		{"units=0-1", 100, 0, 0, false},
		{"bytes=1", 100, 0, 0, false},
		{"bytes=-5", 100, 0, 0, false},
		{"bytes=x-2", 100, 0, 0, false},
		{"bytes=-1-2", 100, 0, 0, false},
		{"bytes=100-", 100, 0, 0, false},
		{"bytes=10-2", 100, 0, 0, false},
		{"bytes=0-x", 100, 0, 0, false},
		{"bytes=0-1,2-3", 100, 0, 0, false},
	} {
		start, end, ok := xetRequestedRange(tc.value, tc.size)
		if ok != tc.ok || ok && (start != tc.start || end != tc.end) {
			t.Errorf("range %q: %d-%d/%v", tc.value, start, end, ok)
		}
	}
	file := xetRecord{
		Segments: []xetSegment{
			{Hash: "a", Start: 1, End: 3},
			{Hash: "a", Start: 3, End: 5},
			{Hash: "a", Start: 7, End: 9},
			{Hash: "b", Start: 5, End: 7},
		},
	}
	for _, tc := range []struct {
		start, end int
		ok         bool
	}{{1, 5, true}, {2, 4, true}, {7, 9, true}, {0, 1, false}, {1, 6, false}, {4, 8, false}, {-1, 1, false}, {1, 1, false}, {0, 9000, false}} {
		if xetContainsChunks(file, "a", tc.start, tc.end) != tc.ok {
			t.Errorf("chunk authority %d:%d", tc.start, tc.end)
		}
	}
}

func TestUnitXetCapabilitiesAreBoundedSignedAndExpiring(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte("x"), 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	h := handler{o: Options{Keys: keys, Now: func() time.Time { return now }}}
	claim := xetClaim{
		Repo:      "hf",
		Member:    "hf",
		Kind:      "models",
		Owner:     "acme",
		Name:      "demo",
		Reference: "main",
		Revision:  "main",
		Subject:   "admin",
		Issued:    now.Unix(),
		Expires:   now.Add(xetTTL).Unix(),
	}
	if got, ok := h.verifyXet(h.signXet(claim)); !ok || got.Subject != "admin" {
		t.Fatal("valid capability rejected")
	}
	if _, ok := (handler{}).verifyXet("x"); ok {
		t.Fatal("unsigned capability accepted")
	}
	for _, raw := range []string{"", strings.Repeat("a", maxXetTokenSize+1), "a.b.c", "a.!", h.signXet(claim) + "x"} {
		if _, ok := h.verifyXet(raw); ok {
			t.Fatal("invalid capability accepted")
		}
	}
	if (handler{}).signXet(claim) != "" {
		t.Fatal("capability issued without a signing key")
	}
	tooLarge := claim
	tooLarge.Subject = strings.Repeat("x", maxXetTokenSize)
	if h.signXet(tooLarge) != "" {
		t.Fatal("oversized capability issued")
	}
	for _, change := range []func(*xetClaim){func(c *xetClaim) { c.Expires = now.Unix() }, func(c *xetClaim) { c.Issued++ }, func(c *xetClaim) { c.Reference = "../secret" }, func(c *xetClaim) { c.File = "../secret" }, func(c *xetClaim) { c.Kind = "spaces" }, func(c *xetClaim) { c.Subject = "" }} {
		bad := claim
		change(&bad)
		if _, ok := h.verifyXet(h.signXet(bad)); ok {
			t.Fatal("invalid claim accepted")
		}
	}
	for _, payload := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte("not json"))} {
		mac := hmac.New(sha256.New, keys.Derive(xetPurpose))
		_, _ = mac.Write([]byte(payload))
		raw := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if _, ok := h.verifyXet(raw); ok {
			t.Fatal("signed malformed payload accepted")
		}
	}
}

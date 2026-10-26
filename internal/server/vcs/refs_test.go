package vcs

import (
	"fmt"
	"strings"
	"testing"
)

func packet(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func TestUnitParseRefsKeepsBranchesAndPeeledTagCommits(t *testing.T) {
	const tag = "0123456789abcdef0123456789abcdef01234567"
	const commit = "abcdef0123456789abcdef0123456789abcdef01"
	data := packet("# service=git-upload-pack\n") + "0000" +
		packet(tag+" HEAD\x00multi_ack thin-pack\n") +
		packet(commit+" refs/heads/main\n") +
		packet(tag+" refs/tags/v1.0.0\n") +
		packet(commit+" refs/tags/v1.0.0^{}\n") + "0000"
	refs, err := ParseRefs([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0] != (Ref{"main", commit, true}) ||
		refs[1] != (Ref{"v1.0.0", commit, false}) {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestUnitParseRefsRefusesMalformedOrAmbiguousAdvertisement(t *testing.T) {
	const oid = "0123456789abcdef0123456789abcdef01234567"
	for name, data := range map[string]string{
		"bad length":       "ffff",
		"bad oid":          packet(strings.Repeat("g", 40) + " refs/heads/main\n"),
		"path traversal":   packet(oid + " refs/heads/../private\n"),
		"duplicate":        packet(oid+" refs/tags/v1\n") + packet(oid+" refs/tags/v1\n"),
		"unmatched peeled": packet(oid + " refs/tags/v1^{}\n"),
		"control":          packet(oid + " refs/heads/a\x01b\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRefs([]byte(data)); err == nil {
				t.Fatal("accepted invalid refs")
			}
		})
	}
}

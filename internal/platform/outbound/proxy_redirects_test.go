package outbound

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnitProxyRedirectHosts(t *testing.T) {
	for _, raw := range []string{"Example.COM. example.com,127.0.0.1;::1\nother.example", "", ", ; \n"} {
		hosts, err := ProxyRedirectHosts(raw)
		if err != nil {
			t.Fatal(err)
		}
		if raw != "" && strings.Contains(raw, "Example") && (len(hosts) != 5 || !hosts["example.com."] || !hosts["example.com"] || !hosts["::1"]) {
			t.Fatal(hosts)
		}
	}
	for _, raw := range []string{"*", "*.example.com", "https://example.com", "example.com:8080", "user@example.com", "example.com/path", "a\tb", "a\rb", "a..b", "-a.example", "a-.example", "a_b", "[::1]", "bad%2eexample", "a?b", "a#b", "a\\b", strings.Repeat("a", 64), strings.Repeat("a.", 127) + "a"} {
		t.Run(strconv.Quote(raw), func(t *testing.T) {
			if _, err := ProxyRedirectHosts(raw); !errors.Is(err, ErrConfiguration) {
				t.Fatal("accepted malformed credential host")
			}
		})
	}
	for c := rune(0); c <= ' '; c++ {
		hosts, err := ProxyRedirectHosts(string(c) + "example.com" + string(c))
		if err != nil || !hosts["example.com"] || len(hosts) != 1 {
			t.Errorf("canonical edge trim rejected U+%04X", c)
		}
	}
	for _, c := range []rune{'\u0085', '\u00a0', '\u1680', '\u2000', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000'} {
		for _, raw := range []string{string(c) + "example.com", "example.com" + string(c)} {
			t.Run(strconv.Quote(raw), func(t *testing.T) {
				if _, err := ProxyRedirectHosts(raw); !errors.Is(err, ErrConfiguration) {
					t.Fatal("Unicode whitespace became a credential grant")
				}
			})
		}
	}
}

func TestUnitProxyRedirectUnicodeASCIIKeys(t *testing.T) {
	// The source UnicodeData/SpecialCasing audit finds Kelvin as the only
	// non-ASCII rune whose full root lowercase becomes an ASCII host label.
	// This also refuses broader Fold/IDNA aliases such as long s and ligatures.
	for r := rune(utf8.RuneSelf); r <= utf8.MaxRune; r++ {
		want := ""
		if r == '\u212a' {
			want = "k.example"
		}
		if got := ProxyRedirectHost(string(r) + ".example"); got != want {
			t.Fatalf("U+%04X credential key %q, want %q", r, got, want)
		}
	}
	for _, host := range []string{"A\u0130.example", "I\u0307.example", "\u0130\u0307.example", "OS\u03a3.example"} {
		t.Run(strconv.Quote(host), func(t *testing.T) {
			if ProxyRedirectHost(host) != "" {
				t.Fatalf("non-ASCII full lowercase became a host grant: %q", host)
			}
		})
	}
}

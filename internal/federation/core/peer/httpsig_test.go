package peer

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rfcKey is RFC 9421's test-key-ed25519 (appendix B.1.4), its JWK d and x.
func rfcKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := base64.RawURLEncoding.DecodeString("n4Ni-HpISpVObnQMW0wOhCKROaIKqKtW_2ZYb2p9KcU")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.RawURLEncoding.DecodeString("JrQLj5P_89iXES9-vFgrIy29clF9CC_oPPsw3c5D0bs")
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !priv.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) { //nolint:forcetypeassert // ed25519 public keys are this type
		t.Fatal("the RFC's d and x are not one key pair")
	}
	return pub, priv
}

// rfcRequest is RFC 9421's test-request (appendix B.2).
func rfcRequest() Message {
	h := http.Header{}
	h.Set("Host", "example.com")
	h.Set("Date", "Tue, 20 Apr 2021 02:07:55 GMT")
	h.Set("Content-Type", "application/json")
	h.Set("Content-Digest", "sha-512=:WZDPaVn/7XgHaAy8pmojAkGWoRx2UFChF41A2svX+TaPm+AbwAgBWnrIiYllu7BNNyealdVLvRwEmTHWXvJwew==:")
	h.Set("Content-Length", "18")
	return Message{Method: "POST", TargetURI: "https://example.com/foo?param=Value&Pet=dog", Header: h}
}

// The signature base, Signature-Input and Signature of RFC 9421's
// appendix B.2.6, "Signing a Request Using ed25519", without its line
// wrapping.
const (
	rfcBase = `"date": Tue, 20 Apr 2021 02:07:55 GMT
"@method": POST
"@path": /foo
"@authority": example.com
"content-type": application/json
"content-length": 18
"@signature-params": ("date" "@method" "@path" "@authority" "content-type" "content-length");created=1618884473;keyid="test-key-ed25519"`
	rfcInput = `sig-b26=("date" "@method" "@path" "@authority" "content-type" "content-length");` +
		`created=1618884473;keyid="test-key-ed25519"`
	rfcSignature = `sig-b26=:wqcAqbmYJ2ji2glfAMaRy4gruYYnx2nEFN2HN6jrnDnQCK1u02Gb04v9EDgwUPiu4A0w6vuQv5lIp5WPpBKRCw==:`
)

func rfcCovered() []Component {
	return []Component{
		{Name: "date"},
		{Name: "@method"},
		{Name: "@path"},
		{Name: "@authority"},
		{Name: "content-type"},
		{Name: "content-length"},
	}
}

// TestUnitRFC9421Ed25519Vector signs RFC 9421's test request with its
// Ed25519 test key and gets the RFC's signature base and, Ed25519 being
// deterministic, its exact signature; and verifies the RFC's own fields.
func TestUnitRFC9421Ed25519Vector(t *testing.T) {
	pub, priv := rfcKey(t)
	m := rfcRequest()
	list := innerList{params: Params{Created: time.Unix(1618884473, 0), KeyID: "test-key-ed25519"}.list()}
	for _, c := range rfcCovered() {
		list.items = append(list.items, c.item())
	}
	base, err := signatureBase(m, list)
	if err != nil {
		t.Fatal(err)
	}
	if base != rfcBase {
		t.Errorf("signature base\n%s\nwant\n%s", base, rfcBase)
	}
	input, sig, err := Sign(m, "sig-b26", rfcCovered(),
		Params{Created: time.Unix(1618884473, 0), KeyID: "test-key-ed25519"}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if input != rfcInput || sig != rfcSignature {
		t.Errorf("Sign = %s\n%s\nwant %s\n%s", input, sig, rfcInput, rfcSignature)
	}

	m.Header.Set(HeaderSignatureInput, rfcInput)
	m.Header.Set(HeaderSignature, rfcSignature)
	parsed, err := ParseSignature(m.Header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Label != "sig-b26" || parsed.Params.KeyID != "test-key-ed25519" ||
		parsed.Params.Created.Unix() != 1618884473 || !parsed.Covers(Component{Name: "@authority"}) {
		t.Errorf("ParseSignature = %+v", parsed)
	}
	if err := parsed.Verify(m, pub); err != nil {
		t.Errorf("the RFC's signature does not verify: %v", err)
	}
	altered := rfcRequest()
	altered.TargetURI = "https://example.com/bar?param=Value&Pet=dog"
	if err := parsed.Verify(altered, pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a changed path verified: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if err := parsed.Verify(m, other); !errors.Is(err, ErrBadSignature) {
		t.Errorf("another key verified: %v", err)
	}
	if err := parsed.Verify(m, nil); !errors.Is(err, ErrBadSignature) {
		t.Errorf("no key verified: %v", err)
	}
}

// TestUnitDerivedComponents derives each supported component of RFC 9421,
// section 2.2, and refuses the ones that do not apply.
func TestUnitDerivedComponents(t *testing.T) {
	req := Message{Method: "GET", TargetURI: "https://Example.COM:443/a%2Fb?x=1&y", Header: http.Header{}}
	for name, want := range map[string]string{
		"@method":         "GET",
		"@target-uri":     "https://Example.COM:443/a%2Fb?x=1&y",
		"@authority":      "example.com",
		"@scheme":         "https",
		"@request-target": "/a%2Fb?x=1&y",
		"@path":           "/a%2Fb",
		"@query":          "?x=1&y",
	} {
		got, err := req.value(Component{Name: name})
		if err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for target, want := range map[string]string{
		"http://h:8080":      "h:8080",
		"http://h:80/":       "h",
		"https://[::1]:8443": "[::1]:8443",
	} {
		if got, _ := (Message{Method: "GET", TargetURI: target}).value(Component{Name: "@authority"}); got != want {
			t.Errorf("@authority of %s = %q, want %q", target, got, want)
		}
	}
	if got, _ := (Message{Method: "GET", TargetURI: "http://h"}).value(Component{Name: "@path"}); got != "/" {
		t.Errorf("an empty path is %q, want /", got)
	}
	h := http.Header{}
	h.Add("X-Many", " a ")
	h.Add("X-Many", "b")
	resp := Message{Status: 204, Header: h, Request: &req}
	for _, c := range []struct {
		c    Component
		want string
	}{
		{Component{Name: "@status"}, "204"},
		{Component{Name: "x-many"}, "a, b"},
		{Component{Name: "@method", Req: true}, "GET"},
	} {
		if got, err := resp.value(c.c); err != nil || got != c.want {
			t.Errorf("%+v = %q, %v; want %q", c.c, got, err, c.want)
		}
	}
	if h.Values("X-Many")[0] != " a " {
		t.Error("deriving a field value changed the message")
	}
	for _, bad := range []struct {
		m Message
		c Component
	}{
		{req, Component{Name: "@status"}},
		{req, Component{Name: "@method", Req: true}},
		{req, Component{Name: "@query-param"}},
		{req, Component{Name: "absent"}},
		{resp, Component{Name: "@method"}},
		{Message{Method: "GET", TargetURI: "/relative"}, Component{Name: "@path"}},
	} {
		if _, err := bad.m.value(bad.c); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%+v on %+v: %v, want ErrBadSignature", bad.c, bad.m, err)
		}
	}
}

// TestUnitParseSignatureRefusals refuses every malformed or ambiguous
// signature field before any key is consulted.
func TestUnitParseSignatureRefusals(t *testing.T) {
	good := `peer=("@method");created=1;keyid="k";alg="ed25519";nonce="n";tag="t";expires=2;other=?1`
	sig := `peer=:AAAA:`
	if s, err := ParseSignature(http.Header{HeaderSignatureInput: {good}, HeaderSignature: {sig}}); err != nil ||
		s.Params.Tag != "t" || s.Params.Expires.Unix() != 2 || s.Params.Alg != "ed25519" || s.Params.Nonce != "n" {
		t.Fatalf("ParseSignature = %+v, %v", s, err)
	}
	for name, h := range map[string]http.Header{
		"no input":             {HeaderSignature: {sig}},
		"no signature":         {HeaderSignatureInput: {good}},
		"two signatures":       {HeaderSignatureInput: {good, `b=("@method")`}, HeaderSignature: {sig, `b=:AAAA:`}},
		"labels differ":        {HeaderSignatureInput: {good}, HeaderSignature: {`other=:AAAA:`}},
		"input is an item":     {HeaderSignatureInput: {`peer="x"`}, HeaderSignature: {sig}},
		"signature not bytes":  {HeaderSignatureInput: {good}, HeaderSignature: {`peer="x"`}},
		"component not string": {HeaderSignatureInput: {`peer=(method)`}, HeaderSignature: {sig}},
		"component parameter":  {HeaderSignatureInput: {`peer=("x";sf)`}, HeaderSignature: {sig}},
		"created not integer":  {HeaderSignatureInput: {`peer=();created="1"`}, HeaderSignature: {sig}},
		"keyid not string":     {HeaderSignatureInput: {`peer=();keyid=1`}, HeaderSignature: {sig}},
		"malformed":            {HeaderSignatureInput: {`peer=(`}, HeaderSignature: {sig}},
	} {
		if _, err := ParseSignature(h); !errors.Is(err, ErrUnsigned) {
			t.Errorf("%s: %v, want ErrUnsigned", name, err)
		}
	}
	dup := Signature{list: innerList{items: []item{{value: "@method"}, {value: "@method"}}}}
	if err := dup.Verify(Message{Method: "GET", TargetURI: "http://h"}, make(ed25519.PublicKey, ed25519.PublicKeySize)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a component covered twice: %v", err)
	}
	if _, _, err := Sign(Message{}, "l", nil, Params{KeyID: "\n"}, nil); err == nil {
		t.Error("a non-printable keyid was signed")
	}
	if _, _, err := Sign(Message{}, "l", []Component{{Name: "@method"}}, Params{}, nil); err == nil {
		t.Error("a component the message lacks was signed")
	}
}

// TestUnitStructuredFields parses and serializes the RFC 8941 forms the
// signature fields use, and refuses malformed ones.
func TestUnitStructuredFields(t *testing.T) {
	d, err := parseDictionary(` a=1, b=?0, c, d=tok/x:y;p=-2, e=:AQI=:, f=("s\"q\\" 3);q, a=2`)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, m := range d {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(m.key)
		switch v := m.value.(type) {
		case innerList:
			b.WriteByte('=')
			serializeInnerList(&b, v)
		case item:
			if v.value == true {
				serializeParams(&b, v.params)
				continue
			}
			b.WriteByte('=')
			serializeItem(&b, v)
		}
	}
	want := `a=2, b=?0, c, d=tok/x:y;p=-2, e=:AQI=:, f=("s\"q\\" 3);q`
	if b.String() != want {
		t.Errorf("round trip = %s, want %s", b.String(), want)
	}
	if v, _ := d.get("b"); v.(item).value != false { //nolint:forcetypeassert // b is an item above
		t.Error("?0 is false")
	}
	if _, ok := d.get("missing"); ok {
		t.Error("get found a missing key")
	}
	if l, err := parseInnerList(`("a");x=1`); err != nil || len(l.items) != 1 {
		t.Errorf("parseInnerList = %+v, %v", l, err)
	}
	for _, bad := range []string{
		`a=1,`, `a=1 b`, `A=1`, `a=1.5`, `a=1234567890123456`, `a=-`, `a="x`, `a="\x"`, "a=\"\x01\"",
		`a=:AAAA`, `a=:!!:`, `a=?2`, `a=@`, `a=("x"`, `a=("x"y)`, `a=1;B`, `a=1;p=@`,
	} {
		if _, err := parseDictionary(bad); !errors.Is(err, errStructured) {
			t.Errorf("parseDictionary(%q): %v, want a refusal", bad, err)
		}
	}
	for _, bad := range []string{`"x"`, `("x") y`, `("x";=)`} {
		if _, err := parseInnerList(bad); !errors.Is(err, errStructured) {
			t.Errorf("parseInnerList(%q): %v, want a refusal", bad, err)
		}
	}
	var bs strings.Builder
	serializeBare(&bs, true)
	if bs.String() != "?1" {
		t.Errorf("true serializes as %s", bs.String())
	}
}

package outbound

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestUnitNTLMIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, user, domain, workstation string
		invalid                         bool
	}{
		{name: "configured", user: "user", domain: "CORP", workstation: "NODE"},
		{name: "workstation-only", user: "user", workstation: "NODE"},
		{name: "domain-only", user: "user", domain: "CORP"},
		{name: "qualified-account", user: `CORP\user`, workstation: "NODE"},
		{name: "missing-account", domain: "CORP", invalid: true},
		{name: "bad-domain", user: "user", domain: `CORP\user`, invalid: true},
		{name: "ambiguous-account", user: `OTHER\user`, domain: "CORP", invalid: true},
		{name: "control", user: "user", workstation: "node\r\n", invalid: true},
		{name: "invalid-utf8", user: "user", domain: string([]byte{255}), invalid: true},
		{name: "long-name", user: "user", workstation: strings.Repeat("n", 256), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, err := NewNTLMProxy(tc.user, "secret", tc.domain, tc.workstation)
			if tc.invalid {
				if !errors.Is(err, ErrConfiguration) || identity != nil {
					t.Fatal("invalid identity accepted")
				}
				return
			}
			if err != nil || identity.domain != tc.domain || identity.workstation != tc.workstation {
				t.Fatal("identity changed")
			}
		})
	}
}

func testNTLMChallenge(flags uint32, info []byte) []byte {
	packet := make([]byte, 48+len(info))
	copy(packet, "NTLMSSP\x00")
	binary.LittleEndian.PutUint32(packet[8:], 2)
	binary.LittleEndian.PutUint32(packet[20:], flags)
	binary.LittleEndian.PutUint16(packet[40:], uint16(len(info)))
	binary.LittleEndian.PutUint32(packet[44:], 48)
	copy(packet[48:], info)
	return packet
}

func TestUnitNTLMChallengeCapabilities(t *testing.T) {
	const flags = 0x00880201
	for _, tc := range []struct {
		name   string
		packet []byte
		valid  bool
	}{
		{"v2", testNTLMChallenge(flags, []byte{0, 0, 0, 0}), true},
		{"no-target-info", testNTLMChallenge(flags, nil), true},
		{"timestamp", testNTLMChallenge(flags, append([]byte{7, 0, 8, 0}, make([]byte, 12)...)), true},
		{"empty-flags", testNTLMChallenge(flags, []byte{6, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0}), true},
		{"mic-required", testNTLMChallenge(flags, []byte{6, 0, 4, 0, 2, 0, 0, 0, 0, 0, 0, 0}), false},
		{"bad-flags", testNTLMChallenge(flags, []byte{6, 0, 1, 0, 0, 0, 0, 0, 0}), false},
		{"channel-binding", testNTLMChallenge(flags, []byte{10, 0, 0, 0, 0, 0, 0, 0}), false},
		{"wrong-timestamp", testNTLMChallenge(flags, []byte{7, 0, 1, 0, 0, 0, 0, 0, 0}), false},
		{"truncated", []byte("NTLMSSP"), false},
		{"wrong-type", make([]byte, 48), false},
		{"v1", testNTLMChallenge(0x201, nil), false},
		{"key-exchange", testNTLMChallenge(flags|0x40000000, nil), false},
		{"signing", testNTLMChallenge(flags|0x10, nil), false},
		{"unknown-pair", testNTLMChallenge(flags, []byte{33, 0, 1, 0, 1, 0, 0, 0, 0}), true},
		{"pair-overrun", testNTLMChallenge(flags, []byte{33, 0, 5, 0, 1, 0, 0, 0, 0}), false},
		{"missing-eol", testNTLMChallenge(flags, []byte{33, 0, 0, 0}), false},
		{"bad-eol", testNTLMChallenge(flags, []byte{0, 0, 1, 0, 1}), false},
		{"short-pair", testNTLMChallenge(flags, []byte{1}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := supportedNTLMChallenge(tc.packet); got != tc.valid {
				t.Fatalf("supported=%v want %v", got, tc.valid)
			}
		})
	}
	outOfBounds := testNTLMChallenge(flags, []byte{0, 0, 0, 0})
	binary.LittleEndian.PutUint32(outOfBounds[44:], 0xffffffff)
	if supportedNTLMChallenge(outOfBounds) {
		t.Fatal("out-of-bounds target info accepted")
	}
}

func TestUnitNTLMAuthenticationHeaders(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("test-packet"))
	for _, tc := range []struct {
		name            string
		headers         []string
		required, valid bool
	}{
		{"advertise", []string{"Basic realm=proxy, NTLM"}, false, true},
		{"case-insensitive", []string{"ntlm"}, false, true},
		{"challenge", []string{"NTLM " + token}, true, true},
		{"quoted-realm", []string{`Basic realm="other, NTLM, scheme"`}, false, false},
		{"quoted-token", []string{`Basic realm="other, NTLM ` + token + `, scheme"`}, true, false},
		{"unterminated-realm", []string{`NTLM, Basic realm="unclosed`}, false, false},
		{"escaped-realm", []string{`Basic realm="other, \"quoted\", scheme", NTLM`}, false, true},
		{"missing", nil, false, false},
		{"unsupported", []string{"Negotiate"}, false, false},
		{"missing-token", []string{"NTLM"}, true, false},
		{"early-token", []string{"NTLM " + token}, false, false},
		{"duplicate", []string{"NTLM", "NTLM"}, false, false},
		{"invalid-base64", []string{"NTLM !!!"}, true, false},
		{"extra-fields", []string{"NTLM token token"}, true, false},
		{"oversize", []string{"NTLM " + base64.StdEncoding.EncodeToString(make([]byte, 65537))}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, valid := ntlmChallenge(tc.headers, tc.required)
			if valid != tc.valid {
				t.Fatalf("header accepted=%v want %v", valid, tc.valid)
			}
		})
	}
}

package token

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// header is a token's JOSE header. Only RS256 is issued or accepted, so a
// token cannot choose a weaker algorithm (or "none") for itself.
type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

const algRS256 = "RS256"

// claims are the JWT claims Access issues (decision 0005).
type claims struct {
	Ver string `json:"ver"`
	Sub string `json:"sub"`
	Scp string `json:"scp"`
	Aud string `json:"aud"`
	Iss string `json:"iss"`
	// Exp is omitted for a token that never expires.
	Exp int64  `json:"exp,omitempty"`
	Iat int64  `json:"iat"`
	Jti string `json:"jti"`
	// Registry marks a registry token, which no record backs (IssueRegistry).
	Registry *types.RegistryAccess `json:"registry,omitempty"`
	// Idn carries, on a registry token alone, the identity of the token from
	// an OIDC exchange that obtained it. The exchanged token's own identity
	// lives in its stored record, but nothing stores a registry token, so
	// without the claim its subject would have to be a stored user.
	Idn types.TokenIdentity `json:"idn,omitempty"`
	// Ptk is, on a registry token alone, the id of the stored token that
	// obtained it, checked on every use: nothing stores a registry token,
	// so this is how revoking its parent reaches it.
	Ptk string `json:"ptk,omitempty"`
}

var b64 = base64.RawURLEncoding

// sign returns the compact JWT for c, signed with key under kid.
func sign(key *rsa.PrivateKey, kid string, c claims) (string, error) {
	h, err := json.Marshal(header{Alg: algRS256, Typ: "JWT", Kid: kid})
	if err != nil {
		return "", fmt.Errorf("token: header: %w", err)
	}
	p, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("token: claims: %w", err)
	}
	input := b64.EncodeToString(h) + "." + b64.EncodeToString(cliSafe(p))
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("token: sign: %w", err)
	}
	return input + "." + b64.EncodeToString(sig), nil
}

// parsed is a JWT split into its parts; nothing in it is trusted until the
// signature over input has been checked.
type parsed struct {
	header  header
	payload []byte
	input   string
	sig     []byte
}

func parse(raw string) (parsed, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return parsed{}, errMalformed
	}
	var out parsed
	h, err := b64.DecodeString(parts[0])
	if err != nil || json.Unmarshal(h, &out.header) != nil {
		return parsed{}, errMalformed
	}
	if out.payload, err = b64.DecodeString(parts[1]); err != nil {
		return parsed{}, errMalformed
	}
	if out.sig, err = b64.DecodeString(parts[2]); err != nil {
		return parsed{}, errMalformed
	}
	out.input = parts[0] + "." + parts[1]
	return out, nil
}

func (p parsed) verify(key *rsa.PublicKey) bool {
	sum := sha256.Sum256([]byte(p.input))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], p.sig) == nil
}

// cliSafe makes a JSON payload whose base64url encoding contains no '-' or
// '_', so the JFrog CLI, which decodes the payload with RawStdEncoding (the
// standard alphabet) to read the user name from sub, reads it correctly.
//
// In base64 a character is 62 ('-') or 63 ('_') only when the low six bits
// of the third byte of a group are 111110 or 111111; every other position
// of ASCII input stays below 62. ASCII bytes with those low bits are '>',
// '?', '~' and DEL, and JSON produces them only inside strings, where they
// can be written as \u escapes. Escaping them, and every non-ASCII rune,
// therefore removes '-' and '_' from the encoding outright. Decision 0005
// describes padding with whitespace until the encoding happens to be clean;
// escaping needs no search and cannot fail.
func cliSafe(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b))
	inString, escaped := false, false
	for _, r := range string(b) {
		switch {
		case !inString:
			out.WriteRune(r)
			inString = r == '"'
		case escaped:
			out.WriteRune(r)
			escaped = false
		case r == '\\':
			out.WriteRune(r)
			escaped = true
		case r == '"':
			out.WriteRune(r)
			inString = false
		case r == '>' || r == '?' || r == '~' || r > 0x7e:
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&out, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&out, `\u%04x`, r)
			}
		default:
			out.WriteRune(r)
		}
	}
	return out.Bytes()
}

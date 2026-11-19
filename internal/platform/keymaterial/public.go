package keymaterial

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// CheckPublic validates the complete public field, including every block and
// packet. It never rewrites the value or includes its contents in an error.
// Public exports must check stored values too: older versions admitted secret
// material in this otherwise publicly readable field.
func CheckPublic(pairType, value string) error {
	if pairType == GPG || pairType == ECDSA && armoredOpenPGP(value) {
		if err := checkPublicArmor(value); err != nil {
			return err
		}
	} else if err := checkPublicPEM(value); err != nil {
		return err
	}
	var err error
	switch pairType {
	case GPG:
		_, err = readEntity(value, "public")
	case RSA:
		_, err = parseRSAPublicKey(value)
	case ECDSA:
		_, err = ECDSAPublicKey(value)
	case Ed25519:
		_, err = Ed25519PublicKey(value)
	default:
		err = ErrInvalid
	}
	if err != nil {
		return invalidf("the public key is invalid for its pair type")
	}
	return nil
}

func checkPublicPEM(value string) error {
	remaining := strings.TrimSpace(value)
	if remaining == "" {
		return invalidf("the public key is empty")
	}
	for remaining != "" {
		// pem.Decode deliberately skips leading non-PEM bytes. A public
		// field must not retain those bytes or an unexamined suffix.
		if !strings.HasPrefix(remaining, "-----BEGIN ") {
			return invalidf("the public key contains non-public material")
		}
		first, _, _ := strings.Cut(remaining, "\n")
		first = strings.TrimSpace(first)
		kind := strings.TrimSuffix(strings.TrimPrefix(first, "-----BEGIN "), "-----")
		end := "-----END " + kind + "-----"
		prefix, rest, ok := strings.Cut(remaining, "\n"+end)
		if !ok || rest != "" && !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
			return invalidf("the public key has invalid PEM framing")
		}
		// Decode can skip a malformed outer frame and return an inner
		// public block, discarding material that this field would export.
		if strings.Contains(prefix, "\n-----BEGIN ") {
			return invalidf("the public key has nested PEM framing")
		}
		block, extra := pem.Decode([]byte(prefix + "\n" + end))
		if block == nil || len(bytes.TrimSpace(extra)) != 0 || block.Type != kind {
			return invalidf("the public key is not valid public PEM")
		}
		var err error
		switch block.Type {
		case "PUBLIC KEY":
			_, err = x509.ParsePKIXPublicKey(block.Bytes)
		case "RSA PUBLIC KEY":
			_, err = x509.ParsePKCS1PublicKey(block.Bytes)
		case "CERTIFICATE":
			_, err = x509.ParseCertificate(block.Bytes)
		default:
			return invalidf("the public key contains non-public material")
		}
		if err != nil {
			return invalidf("the public key contains an invalid public block")
		}
		remaining = strings.TrimSpace(rest)
	}
	return nil
}

func checkPublicArmor(value string) error {
	remaining := strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
	for remaining != "" {
		body, rest, err := publicArmorBody(remaining)
		if err != nil {
			return err
		}
		// packet.Reader.Next skips unsupported packets. Read examines each
		// packet, including private subkeys and later entities.
		reader := bytes.NewReader(body)
		for {
			p, err := packet.Read(reader)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return invalidf("the public key contains an invalid packet")
			}
			switch p.(type) {
			case *packet.PublicKey, *packet.Signature, *packet.UserId, *packet.UserAttribute, *packet.Marker:
			default:
				return invalidf("the public key contains non-public packets")
			}
		}
		keys, err := openpgp.ReadKeyRing(bytes.NewReader(body))
		if err != nil || len(keys) == 0 {
			return invalidf("the public key contains no valid public key")
		}
		remaining = strings.TrimSpace(rest)
	}
	if strings.TrimSpace(value) == "" {
		return invalidf("the public key is empty")
	}
	return nil
}

// publicArmorBody checks framing before decoding: armor.Decode may skip leading
// bytes, read beyond the block, and stop at a checksum without checking the rest.
// Those permissive behaviors cannot establish that the complete field is public.
func publicArmorBody(value string) ([]byte, string, error) {
	const begin = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n"
	const end = "-----END PGP PUBLIC KEY BLOCK-----"
	if !strings.HasPrefix(value, begin) {
		return nil, "", invalidf("the public key must use public OpenPGP armor")
	}
	text, rest, ok := strings.Cut(value[len(begin):], "\n"+end)
	if !ok || rest != "" && !strings.HasPrefix(rest, "\n") {
		return nil, "", invalidf("the public key has invalid armor framing")
	}
	var encoded strings.Builder
	headers, checksum := true, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if headers {
			if line == "" {
				headers = false
			} else if !strings.Contains(line, ":") {
				return nil, "", invalidf("the public key has invalid armor headers")
			}
			continue
		}
		if checksum || line == "" {
			return nil, "", invalidf("the public key has invalid armor contents")
		}
		if strings.HasPrefix(line, "=") {
			crc, err := base64.StdEncoding.DecodeString(line[1:])
			if err != nil || len(crc) != 3 {
				return nil, "", invalidf("the public key has an invalid armor checksum")
			}
			checksum = true // go-crypto does not verify the optional CRC.
			continue
		}
		encoded.WriteString(line)
	}
	body, err := base64.StdEncoding.DecodeString(encoded.String())
	if headers || err != nil || len(body) == 0 {
		return nil, "", invalidf("the public key has invalid armor contents")
	}
	return body, rest, nil
}

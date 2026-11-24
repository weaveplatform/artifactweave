package peer

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is RFC 9421's general mechanism: component values, the
// signature base, and creating and checking one Ed25519 signature. Which
// components and parameters a peer signature must carry is policy, applied
// by Verifier and Signer.

// Field names of RFC 9421 and RFC 9530.
const (
	HeaderSignatureInput = "Signature-Input"
	HeaderSignature      = "Signature"
	HeaderContentDigest  = "Content-Digest"
	HeaderDate           = "Date"
)

// Errors a signature check wraps. None of them carries a signature, key or
// nonce, so a refusal can be logged and answered as it is.
var (
	// ErrUnsigned: Signature or Signature-Input is missing, malformed, or
	// names more than one signature.
	ErrUnsigned = errors.New("the message carries no usable signature")
	// ErrBadSignature: the signature does not verify, or a component it
	// covers cannot be derived from the message.
	ErrBadSignature = errors.New("the signature does not verify")
	// ErrSigning: a signature could not be made, for want of a key or a
	// printable parameter.
	ErrSigning = errors.New("the message cannot be signed")
)

// Component is a covered component identifier: a derived component such as
// "@method" or a lower-case field name, with RFC 9421's req flag for a
// request component a response signature covers.
type Component struct {
	Name string
	Req  bool
}

func (c Component) item() item {
	it := item{value: c.Name}
	if c.Req {
		it.params = params{{key: "req", value: true}}
	}
	return it
}

// Message is what a signature covers. A request sets Method and TargetURI;
// a response sets Status and Request, the request it answers, for
// components flagged req.
type Message struct {
	Method    string
	TargetURI string
	Status    int
	Header    http.Header
	Request   *Message
}

// value derives a component's value from m (RFC 9421, section 2).
func (m Message) value(c Component) (string, error) {
	if c.Req {
		if m.Request == nil {
			return "", fmt.Errorf("%w: %s;req on a request", ErrBadSignature, c.Name)
		}
		return m.Request.value(Component{Name: c.Name})
	}
	if !strings.HasPrefix(c.Name, "@") {
		lines := m.Header.Values(c.Name)
		if len(lines) == 0 {
			return "", fmt.Errorf("%w: the %s field is absent", ErrBadSignature, c.Name)
		}
		// Values returns the header's own slice; trimming it in place would
		// change the message.
		values := make([]string, len(lines))
		for i, v := range lines {
			values[i] = strings.TrimSpace(v)
		}
		return strings.Join(values, ", "), nil
	}
	if c.Name == "@status" {
		if m.Status == 0 {
			return "", fmt.Errorf("%w: @status on a request", ErrBadSignature)
		}
		return strconv.Itoa(m.Status), nil
	}
	if m.Method == "" {
		return "", fmt.Errorf("%w: %s on a response", ErrBadSignature, c.Name)
	}
	if c.Name == "@method" {
		return m.Method, nil
	}
	u, err := url.Parse(m.TargetURI)
	if err != nil || !u.IsAbs() {
		return "", fmt.Errorf("%w: the target URI is not absolute", ErrBadSignature)
	}
	switch c.Name {
	case "@target-uri":
		return m.TargetURI, nil
	case "@authority":
		return authority(u), nil
	case "@scheme":
		return strings.ToLower(u.Scheme), nil
	case "@request-target":
		return u.RequestURI(), nil
	case "@path":
		if p := u.EscapedPath(); p != "" {
			return p, nil
		}
		return "/", nil
	case "@query":
		return "?" + u.RawQuery, nil
	default:
		return "", fmt.Errorf("%w: unsupported component %s", ErrBadSignature, c.Name)
	}
}

// authority is RFC 9421's @authority: the host in lower case, with the port
// only when it is not the scheme's default.
func authority(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	scheme := strings.ToLower(u.Scheme)
	if port == "" || scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		return host
	}
	return host + ":" + port
}

// signatureBase renders RFC 9421's signature base (section 2.5) for the
// covered components and signature parameters of list.
func signatureBase(m Message, list innerList) (string, error) {
	var b strings.Builder
	seen := make(map[string]bool, len(list.items))
	for _, it := range list.items {
		c, err := componentOf(it)
		if err != nil {
			return "", err
		}
		var id strings.Builder
		serializeItem(&id, it)
		if seen[id.String()] {
			return "", fmt.Errorf("%w: %s is covered twice", ErrBadSignature, id.String())
		}
		seen[id.String()] = true
		v, err := m.value(c)
		if err != nil {
			return "", err
		}
		b.WriteString(id.String())
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteByte('\n')
	}
	b.WriteString(`"@signature-params": `)
	serializeInnerList(&b, list)
	return b.String(), nil
}

// componentOf reads a covered component identifier; only the req parameter
// is supported.
func componentOf(it item) (Component, error) {
	name, ok := it.value.(string)
	if !ok || name == "" {
		return Component{}, fmt.Errorf("%w: a covered component is not a string", ErrUnsigned)
	}
	c := Component{Name: name}
	for _, p := range it.params {
		if p.key != "req" || p.value != true {
			return Component{}, fmt.Errorf("%w: unsupported component parameter %s", ErrUnsigned, p.key)
		}
		c.Req = true
	}
	return c, nil
}

// Params are a signature's metadata parameters (RFC 9421, section 2.3).
// Zero values are left out; they serialize in the order created, expires,
// keyid, alg, nonce, tag.
type Params struct {
	Created time.Time
	Expires time.Time
	KeyID   string
	Alg     string
	Nonce   string
	Tag     string
}

func (p Params) list() params {
	var out params
	if !p.Created.IsZero() {
		out = append(out, param{key: "created", value: p.Created.Unix()})
	}
	if !p.Expires.IsZero() {
		out = append(out, param{key: "expires", value: p.Expires.Unix()})
	}
	for _, kv := range [][2]string{{"keyid", p.KeyID}, {"alg", p.Alg}, {"nonce", p.Nonce}, {"tag", p.Tag}} {
		if kv[1] != "" {
			out = append(out, param{key: kv[0], value: kv[1]})
		}
	}
	return out
}

// Sign signs m over covered with key and returns the Signature-Input and
// Signature field values under label.
func Sign(
	m Message,
	label string,
	covered []Component,
	p Params,
	key ed25519.PrivateKey,
) (input, signature string, err error) {
	ps := p.list()
	for _, x := range ps {
		if s, ok := x.value.(string); ok && !validString(s) {
			return "", "", fmt.Errorf("%w: parameter %s is not printable ASCII", ErrSigning, x.key)
		}
	}
	list := innerList{params: ps}
	for _, c := range covered {
		list.items = append(list.items, c.item())
	}
	base, err := signatureBase(m, list)
	if err != nil {
		return "", "", err
	}
	var in, sig strings.Builder
	in.WriteString(label)
	in.WriteByte('=')
	serializeInnerList(&in, list)
	sig.WriteString(label)
	sig.WriteByte('=')
	serializeBare(&sig, ed25519.Sign(key, []byte(base)))
	return in.String(), sig.String(), nil
}

// Signature is one signature read from a message's Signature-Input and
// Signature fields.
type Signature struct {
	Label   string
	Covered []Component
	Params  Params
	list    innerList
	value   []byte
}

// Covers reports whether the signature covers c.
func (s Signature) Covers(c Component) bool {
	for _, x := range s.Covered {
		if x == c {
			return true
		}
	}
	return false
}

// ParseSignature reads the one signature h carries. A message with no
// signature, with several, or whose fields do not parse gives ErrUnsigned.
func ParseSignature(h http.Header) (Signature, error) {
	inputs, err := dictionaryField(h, HeaderSignatureInput)
	if err != nil {
		return Signature{}, err
	}
	values, err := dictionaryField(h, HeaderSignature)
	if err != nil {
		return Signature{}, err
	}
	// The peer protocol sends exactly one signature. Accepting several would
	// leave which one authenticates the message to chance (RFC 9421, section
	// 7.2.6).
	if len(inputs) != 1 || len(values) != 1 || inputs[0].key != values[0].key {
		return Signature{}, fmt.Errorf("%w: want exactly one signature", ErrUnsigned)
	}
	list, ok := inputs[0].value.(innerList)
	if !ok {
		return Signature{}, fmt.Errorf("%w: Signature-Input is not an inner list", ErrUnsigned)
	}
	sig, ok := values[0].value.(item)
	raw, isBytes := sig.value.([]byte)
	if !ok || !isBytes {
		return Signature{}, fmt.Errorf("%w: Signature is not a byte sequence", ErrUnsigned)
	}
	out := Signature{Label: inputs[0].key, list: list, value: raw}
	for _, it := range list.items {
		c, err := componentOf(it)
		if err != nil {
			return Signature{}, err
		}
		out.Covered = append(out.Covered, c)
	}
	if out.Params, err = readParams(list.params); err != nil {
		return Signature{}, err
	}
	return out, nil
}

func dictionaryField(h http.Header, name string) (dictionary, error) {
	values := h.Values(name)
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: %s is missing", ErrUnsigned, name)
	}
	d, err := parseDictionary(strings.Join(values, ", "))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnsigned, name, err)
	}
	return d, nil
}

func readParams(ps params) (Params, error) {
	var out Params
	for _, p := range ps {
		switch p.key {
		case "created", "expires":
			n, ok := p.value.(int64)
			if !ok {
				return Params{}, fmt.Errorf("%w: %s is not an integer", ErrUnsigned, p.key)
			}
			if p.key == "created" {
				out.Created = time.Unix(n, 0)
			} else {
				out.Expires = time.Unix(n, 0)
			}
		case "keyid", "alg", "nonce", "tag":
			s, ok := p.value.(string)
			if !ok {
				return Params{}, fmt.Errorf("%w: %s is not a string", ErrUnsigned, p.key)
			}
			switch p.key {
			case "keyid":
				out.KeyID = s
			case "alg":
				out.Alg = s
			case "nonce":
				out.Nonce = s
			default:
				out.Tag = s
			}
		default:
			// RFC 9421 registers no other parameter; an unknown one is
			// still covered by the signature, so it is kept and ignored.
		}
	}
	return out, nil
}

// Verify checks the signature over m with key. It checks no parameter: the
// caller applies its own requirements to s.Params and s.Covered.
func (s Signature) Verify(m Message, key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no Ed25519 key", ErrBadSignature)
	}
	base, err := signatureBase(m, s.list)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, []byte(base), s.value) {
		return ErrBadSignature
	}
	return nil
}

package fakeupstream

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // Independent NTLMv2 protocol verifier, never application password storage.
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // NTLM's specified password hash in a controlled fixture.
)

// NTLMOptions names fixture-only credentials and protocol faults. Challenge,
// when non-nil, replaces the valid type-2 packet; DropAfterNegotiate interrupts
// the exchange before authentication. TLS configures the proxy's own identity.
type NTLMOptions struct {
	Username, Password, Domain, Workstation string
	Challenge                               []byte
	DropAfterNegotiate                      bool
	CloseAfterChallenge                     bool
	BeforeChallenge                         *Barrier
	TLS                                     *tls.Config
	ChallengeBody                           []byte
	InitialAuthenticate                     string
	Forward                                 bool
	ForwardOnly                             bool
	// Listener, when set, is owned by the proxy, as with NewProxyOn.
	Listener net.Listener
}

// NTLMRequest is a safe protocol record. It retains no packet, password, proof
// or hash. Connection identifies the local client socket, not a user identity.
type NTLMRequest struct {
	Connection          string
	Message             uint32
	Domain, Workstation string
	Authorized          bool
	OriginAuthorization bool
}

// NTLMProxy is a bounded CONNECT proxy that independently checks NTLMv2 proofs
// using MS-NLMP's ResponseKeyNT and NTProofStr. The production client library is
// deliberately absent here, so acceptance cannot pass by trusting its output.
type NTLMProxy struct {
	*Server
	options       NTLMOptions
	mu            sync.Mutex
	challenges    map[string]ntlmChallengeState
	requests      []NTLMRequest
	closed        map[string]chan struct{}
	authenticated map[string]bool
}

type ntlmChallengeState struct{ nonce, info []byte }

// NewNTLMProxy starts a connection-oriented NTLMv2 proxy restricted to targets.
func NewNTLMProxy(t testing.TB, options NTLMOptions, targets ...*Server) *NTLMProxy {
	t.Helper()
	p := &NTLMProxy{options: options, challenges: make(map[string]ntlmChallengeState), closed: make(map[string]chan struct{}), authenticated: make(map[string]bool)}
	p.Server = newProxy(t, Options{TLS: options.TLS, Secrets: []string{options.Password}, listener: options.Listener}, p, targets...)
	return p
}

// Negotiations returns safe, independent protocol records in arrival order.
func (p *NTLMProxy) Negotiations() []NTLMRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]NTLMRequest(nil), p.requests...)
}

// ConnectionClosed closes when the authenticated socket leaves the fixture.
// Call after a Negotiations record has identified the connection.
func (p *NTLMProxy) ConnectionClosed(connection string) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed[connection]
}

// Go's HTTP server deliberately closes every rejected CONNECT connection. A
// real NTLM proxy must keep the 407 exchange on one socket, so the chi entry
// hands that socket to this bounded protocol fixture before any response.
func (p *proxy) serveNTLM(w http.ResponseWriter, r *http.Request) {
	if p.ntlm.options.ForwardOnly || r.Method != http.MethodConnect || p.allowed[r.Host] == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	p.mu.Lock()
	if p.isClosed {
		p.mu.Unlock()
		return
	}
	p.connections[conn] = struct{}{}
	p.wg.Add(1)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.connections, conn)
		p.mu.Unlock()
		p.wg.Done()
	}()
	p.ntlm.mu.Lock()
	closed := make(chan struct{})
	p.ntlm.closed[r.RemoteAddr] = closed
	p.ntlm.mu.Unlock()
	defer close(closed)
	for range 3 {
		response := &ntlmResponse{header: make(http.Header)}
		allowed := p.ntlm.authenticate(response, r)
		p.server.record(r, allowed)
		if allowed {
			upstream, dialErr := (&net.Dialer{}).DialContext(r.Context(), "tcp", r.Host)
			if dialErr != nil {
				return
			}
			defer upstream.Close()
			_ = conn.SetDeadline(time.Time{})
			p.pipeTunnel(conn, buffered, upstream)
			return
		}
		if _, err := fmt.Fprint(buffered, "HTTP/1.1 407 Proxy Authentication Required\r\n"); err != nil {
			return
		}
		if err := response.header.Write(buffered); err != nil {
			return
		}
		if _, err := fmt.Fprint(buffered, "\r\n"); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil {
			return
		}
		if len(p.ntlm.options.ChallengeBody) > 0 {
			if _, err := buffered.Write(p.ntlm.options.ChallengeBody); err != nil {
				return
			}
			if err := buffered.Flush(); err != nil {
				return
			}
		}
		if response.header.Get("Connection") == "close" {
			return
		}
		next, readErr := http.ReadRequest(buffered.Reader)
		if readErr != nil {
			return
		}
		_ = next.Body.Close()
		next.RemoteAddr = r.RemoteAddr
		next = next.WithContext(r.Context())
		if next.Method != http.MethodConnect || next.Host != r.Host {
			return
		}
		r = next
	}
}

type ntlmResponse struct{ header http.Header }

func (w *ntlmResponse) Header() http.Header       { return w.header }
func (*ntlmResponse) WriteHeader(int)             {}
func (*ntlmResponse) Write(b []byte) (int, error) { return len(b), nil }

func (p *NTLMProxy) authenticate(w http.ResponseWriter, r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	record := NTLMRequest{Connection: r.RemoteAddr, OriginAuthorization: r.Header.Get("Authorization") != ""}
	defer func() { p.requests = append(p.requests, record) }()
	packet, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Proxy-Authorization"), "NTLM "))
	if len(packet) >= 12 && bytes.Equal(packet[:8], []byte("NTLMSSP\x00")) {
		record.Message = binary.LittleEndian.Uint32(packet[8:12])
	}
	if record.Message == 0 && p.authenticated[r.RemoteAddr] {
		record.Authorized = true
		return true
	}
	challenge := "NTLM"
	switch record.Message {
	case 1:
		record.Domain = string(ntlmField(packet, 16))
		record.Workstation = string(ntlmField(packet, 24))
		if !strings.EqualFold(record.Domain, p.options.Domain) || !strings.EqualFold(record.Workstation, p.options.Workstation) {
			break
		}
		if p.options.DropAfterNegotiate {
			panic(http.ErrAbortHandler)
		}
		if p.options.BeforeChallenge != nil && p.options.BeforeChallenge.Wait(r.Context()) != nil {
			return false
		}
		serverChallenge := make([]byte, 8)
		if _, err := rand.Read(serverChallenge); err != nil {
			panic(http.ErrAbortHandler)
		}
		info := []byte{}
		for _, pair := range []struct {
			id    uint16
			value string
		}{{1, "PROXY"}, {2, p.options.Domain}} {
			value := ntlmUnicode(pair.value)
			header := make([]byte, 4)
			binary.LittleEndian.PutUint16(header, pair.id)
			size := len(value)
			if size > 65535 {
				panic(http.ErrAbortHandler)
			}
			binary.LittleEndian.PutUint16(header[2:], uint16(size))
			info = append(append(info, header...), value...)
		}
		info = append(info, 0, 0, 0, 0)
		p.challenges[r.RemoteAddr] = ntlmChallengeState{nonce: serverChallenge, info: info}
		size := len(info)
		if size > 65535 {
			panic(http.ErrAbortHandler)
		}
		message := make([]byte, 48+len(info))
		copy(message, "NTLMSSP\x00")
		binary.LittleEndian.PutUint32(message[8:], 2)
		binary.LittleEndian.PutUint32(message[20:], 0x00880201)
		copy(message[24:], serverChallenge)
		binary.LittleEndian.PutUint16(message[40:], uint16(size))
		binary.LittleEndian.PutUint16(message[42:], uint16(size))
		binary.LittleEndian.PutUint32(message[44:], 48)
		copy(message[48:], info)
		if p.options.Challenge != nil {
			message = p.options.Challenge
		}
		challenge += " " + base64.StdEncoding.EncodeToString(message)
		if p.options.CloseAfterChallenge {
			w.Header().Set("Connection", "close")
		}
	case 3:
		record.Domain = ntlmString(packet, 28)
		record.Workstation = ntlmString(packet, 44)
		proof := ntlmField(packet, 20)
		state := p.challenges[r.RemoteAddr]
		delete(p.challenges, r.RemoteAddr)
		if len(proof) >= 48 && len(state.nonce) == 8 && bytes.Equal(proof[16:20], []byte{1, 1, 0, 0}) && bytes.Equal(proof[44:len(proof)-4], state.info) &&
			record.Domain == p.options.Domain && record.Workstation == p.options.Workstation &&
			ntlmString(packet, 36) == p.options.Username {
			// The password and response keys live only inside this assertion;
			// neither is retained in public evidence or request records.
			passwordHash := md4.New() //nolint:gosec // NTLMv2's specified primitive in a test-only dependency fixture.
			_, _ = passwordHash.Write(ntlmUnicode(p.options.Password))
			key := ntlmMAC(passwordHash.Sum(nil), ntlmUnicode(strings.ToUpper(p.options.Username)+p.options.Domain))
			expected := ntlmMAC(key, append(append([]byte(nil), state.nonce...), proof[16:]...))
			record.Authorized = hmac.Equal(proof[:16], expected)
		}
		if record.Authorized {
			p.authenticated[r.RemoteAddr] = true
			return true
		}
	}
	if record.Message == 0 && p.options.InitialAuthenticate != "" {
		challenge = p.options.InitialAuthenticate
	}
	w.Header().Set("Proxy-Authenticate", challenge)
	w.Header().Set("Content-Length", strconv.Itoa(len(p.options.ChallengeBody)))
	w.WriteHeader(http.StatusProxyAuthRequired)
	if len(p.options.ChallengeBody) > 0 {
		_, _ = w.Write(p.options.ChallengeBody)
	}
	return false
}

func ntlmMAC(key, data []byte) []byte {
	m := hmac.New(md5.New, key)
	_, _ = m.Write(data)
	return m.Sum(nil)
}

func ntlmUnicode(s string) []byte {
	words := utf16.Encode([]rune(s))
	data := make([]byte, 2*len(words))
	for i, word := range words {
		binary.LittleEndian.PutUint16(data[2*i:], word)
	}
	return data
}

func ntlmField(packet []byte, offset int) []byte {
	if offset+8 > len(packet) {
		return nil
	}
	n := int(binary.LittleEndian.Uint16(packet[offset:]))
	start := int(binary.LittleEndian.Uint32(packet[offset+4:]))
	if start > len(packet) || n > len(packet)-start {
		return nil
	}
	return packet[start : start+n]
}

func ntlmString(packet []byte, offset int) string {
	data := ntlmField(packet, offset)
	words := make([]uint16, len(data)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return string(utf16.Decode(words))
}

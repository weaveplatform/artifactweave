package outbound

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Azure/go-ntlmssp"
)

// NTLMProxy authenticates individual, already dialed proxy connections.
// It owns no pool and never substitutes the origin Authorization identity.
type NTLMProxy struct {
	username, password, domain, workstation string
}

// NewNTLMProxy validates and snapshots an explicitly configured NTLMv2 identity.
// Domain identifies the proxy account; workstation names this client machine.
// Invalid or ambiguous names fail with ErrConfiguration, without credentials.
// It performs no network I/O and the result is safe for concurrent connections.
func NewNTLMProxy(username, password, domain, workstation string) (*NTLMProxy, error) {
	if username == "" || !validNTLMName(domain) || !validNTLMName(workstation) ||
		!validNTLMName(username) {
		return nil, ErrConfiguration
	}
	if domain != "" {
		if strings.ContainsAny(domain, `\@`) || strings.Contains(username, `\`) {
			return nil, ErrConfiguration
		}
		username = domain + `\` + username
	}
	return &NTLMProxy{username: username, password: password, domain: domain, workstation: workstation}, nil
}

func validNTLMName(s string) bool {
	return utf8.ValidString(s) && len(s) <= 255 && !strings.ContainsAny(s, "\x00\r\n")
}

// Connect negotiates NTLMv2 and CONNECT to a caller-checked physical target.
// The caller owns proxy dialing/TLS and socket inactivity deadlines. Connect
// closes conn on failure and caller cancellation; success transfers the tunnel
// to the caller. Errors preserve context and safe outbound classifications.
// maxHeaderBytes bounds each response header; zero uses 1 MiB. Authentication
// belongs to this connection; origin streaming and TLS stay with the caller.
func (identity *NTLMProxy) Connect(ctx context.Context, conn net.Conn, target string, maxHeaderBytes int64) (net.Conn, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	if maxHeaderBytes <= 0 {
		maxHeaderBytes = 1 << 20
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return nil, ErrConfiguration
	}
	// ReadResponse has no header limit. Reset the reader's allowance for each
	// response, retaining any buffered bytes for the next response or tunnel.
	limited := &io.LimitedReader{R: conn}
	reader := bufio.NewReader(limited)
	for stage := range 3 {
		if err = req.Write(conn); err != nil {
			return nil, SafeError(err)
		}
		limited.N = maxHeaderBytes
		// A successful CONNECT body is the tunnel; on refusal the connection is
		// closed by tunnel's owner, including bodies over the draining bound.
		//nolint:bodyclose // Connection ownership includes every CONNECT response body.
		res, readErr := http.ReadResponse(reader, req)
		if readErr != nil {
			return nil, SafeError(readErr)
		}
		if res.StatusCode == http.StatusOK {
			if stage != 2 {
				return nil, ErrSecurity
			}
			success = true
			return bufferedConn{Conn: conn, reader: io.MultiReader(reader, conn)}, nil
		}
		if res.StatusCode != http.StatusProxyAuthRequired || stage == 2 || res.Close {
			return nil, ErrSecurity
		}
		// A challenge body is bounded separately from artifact bytes, and must
		// finish on this same socket before another handshake message is sent.
		limited.N = 64 << 10
		n, drainErr := io.CopyN(io.Discard, res.Body, 64<<10+1)
		if n > 64<<10 || !errors.Is(drainErr, io.EOF) || res.Body.Close() != nil {
			return nil, ErrSecurity
		}
		authorization, authErr := identity.authorization(res.Header, stage)
		if authErr != nil {
			return nil, authErr
		}
		req.Header.Set("Proxy-Authorization", authorization)
	}
	return nil, ErrSecurity
}

func (identity *NTLMProxy) authorization(headers http.Header, stage int) (string, error) {
	challenge, ok := ntlmChallenge(headers.Values("Proxy-Authenticate"), stage == 1)
	if !ok {
		return "", ErrSecurity
	}
	var token []byte
	var err error
	if stage == 0 {
		token, err = ntlmssp.NewNegotiateMessage(identity.domain, identity.workstation)
	} else {
		if !supportedNTLMChallenge(challenge) {
			return "", ErrSecurity
		}
		token, err = ntlmssp.NewAuthenticateMessage(challenge, identity.username, identity.password, &ntlmssp.AuthenticateMessageOptions{WorkstationName: identity.workstation})
	}
	if err != nil {
		return "", ErrSecurity
	}
	return "NTLM " + base64.StdEncoding.EncodeToString(token), nil
}

func ntlmChallenge(headers []string, tokenRequired bool) ([]byte, bool) {
	var token []byte
	found := false
	for _, header := range headers {
		challenges, valid := proxyChallenges(header)
		if !valid {
			return nil, false
		}
		for _, value := range challenges {
			fields := strings.Fields(value)
			if len(fields) == 0 || !strings.EqualFold(fields[0], "NTLM") {
				continue
			}
			if found || len(fields) > 2 || tokenRequired != (len(fields) == 2) {
				return nil, false
			}
			found = true
			if tokenRequired {
				var err error
				token, err = base64.StdEncoding.Strict().DecodeString(fields[1])
				if err != nil || len(token) > 64<<10 {
					return nil, false
				}
			}
		}
	}
	return token, found
}

// RFC 9110 sections 5.6.4 and 11.3 allow commas inside quoted auth parameters.
// A realm containing "NTLM" must never become a request for proof material.
func proxyChallenges(header string) ([]string, bool) {
	parts := []string{}
	start := 0
	quoted, escaped := false, false
	for i, ch := range header {
		switch {
		case escaped:
			escaped = false
		case quoted && ch == '\\':
			escaped = true
		case ch == '"':
			quoted = !quoted
		case ch == ',' && !quoted:
			parts = append(parts, header[start:i])
			start = i + 1
		}
	}
	if quoted || escaped {
		return nil, false
	}
	return append(parts, header[start:]), true
}

// The dependency supplies NTLMv2 crypto, but does not implement signing,
// encryption, MIC or channel binding. Refuse requests for these capabilities
// before creating proof material; never silently negotiate a weaker mode.
func supportedNTLMChallenge(packet []byte) bool {
	if len(packet) < 48 || !bytes.Equal(packet[:8], []byte("NTLMSSP\x00")) ||
		binary.LittleEndian.Uint32(packet[8:]) != 2 {
		return false
	}
	flags := binary.LittleEndian.Uint32(packet[20:])
	const required = 0x00080201    // Unicode, NTLM and extended-session security.
	const unsupported = 0x400000f0 // Key exchange, LM key, datagram, seal and sign.
	if flags&required != required || flags&unsupported != 0 {
		return false
	}
	length := int(binary.LittleEndian.Uint16(packet[40:]))
	start := int(binary.LittleEndian.Uint32(packet[44:]))
	if length == 0 {
		return true
	}
	if start < 48 || start > len(packet) || length > len(packet)-start {
		return false
	}
	info := packet[start : start+length]
	for len(info) >= 4 {
		id := binary.LittleEndian.Uint16(info)
		n := int(binary.LittleEndian.Uint16(info[2:]))
		info = info[4:]
		if n > len(info) {
			return false
		}
		switch id {
		case 0:
			return n == 0 && len(info) == 0
		case 6: // MsvAvFlags: the dependency cannot supply a required MIC.
			if n != 4 || binary.LittleEndian.Uint32(info)&2 != 0 {
				return false
			}
		case 10: // MsvAvChannelBindings.
			return false
		case 7: // MsvAvTimestamp must be exactly one FILETIME.
			if n != 8 {
				return false
			}
		}
		info = info[n:]
	}
	return false
}

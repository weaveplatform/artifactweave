package ldap_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	goldap "github.com/go-ldap/ldap/v3"

	accessldap "github.com/weaveplatform/artifactweave/internal/access/adapters/ldap"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	coreldap "github.com/weaveplatform/artifactweave/internal/access/core/ldap"
)

// fake is an LDAP server that answers each request as its test says, for
// the faults a real directory will not produce on demand: malformed answers,
// silence, planted Java objects, ambiguous and oversized results.
type fake struct {
	addr string
	mu   sync.Mutex
	// requests are the raw requests received, in order.
	requests [][]byte
}

// answer is what the fake does with one request: the packets it writes, or
// raw bytes, or nothing at all (hang).
type answer struct {
	packets []*ber.Packet
	raw     []byte
	hang    bool
}

// startFake serves handle on a loopback port until the test ends.
func startFake(t *testing.T, handle func(op ber.Tag, id int64) answer) *fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{addr: ln.Addr().String()}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, handle, done)
		}
	}()
	return f
}

func (f *fake) serve(conn net.Conn, handle func(ber.Tag, int64) answer, done chan struct{}) {
	defer func() { _ = conn.Close() }()
	for {
		p, err := ber.ReadPacket(conn)
		if err != nil || len(p.Children) < 2 {
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, p.Bytes())
		f.mu.Unlock()
		id, _ := p.Children[0].Value.(int64)
		op := p.Children[1].Tag
		if op == goldap.ApplicationUnbindRequest {
			return
		}
		a := handle(op, id)
		switch {
		case a.hang:
			<-done
			return
		case a.raw != nil:
			_, _ = conn.Write(a.raw)
		}
		for _, out := range a.packets {
			if _, err := conn.Write(out.Bytes()); err != nil {
				return
			}
		}
	}
}

func (f *fake) sawPaging() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if bytes.Contains(r, []byte(goldap.ControlTypePaging)) {
			return true
		}
	}
	return false
}

func envelope(id int64, op *ber.Packet) *ber.Packet {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "message")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "id"))
	p.AppendChild(op)
	return p
}

func result(id int64, tag ber.Tag, code uint16, message string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "code"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "dn"))
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, message, "message"))
	return envelope(id, op)
}

func entry(id int64, dn string, attrs map[string][]string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, goldap.ApplicationSearchResultEntry, nil, "entry")
	op.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, dn, "dn"))
	list := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attributes")
	for name, values := range attrs {
		a := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "attribute")
		a.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, "type"))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "values")
		for _, v := range values {
			set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, "value"))
		}
		a.AppendChild(set)
		list.AppendChild(a)
	}
	op.AppendChild(list)
	return envelope(id, op)
}

func bindOK(id int64) answer {
	return answer{packets: []*ber.Packet{result(id, goldap.ApplicationBindResponse, 0, "")}}
}

func done(id int64, code uint16) *ber.Packet {
	return result(id, goldap.ApplicationSearchResultDone, code, "")
}

// person is an ordinary entry the fake returns.
var person = map[string][]string{"objectClass": {"inetOrgPerson"}, "mail": {"u@example.org"}}

// searching answers binds with success and searches with entries, then a
// done of code.
func searching(code uint16, entries ...map[string][]string) func(ber.Tag, int64) answer {
	return func(op ber.Tag, id int64) answer {
		if op == goldap.ApplicationBindRequest {
			return bindOK(id)
		}
		var out []*ber.Packet
		for i, attrs := range entries {
			out = append(out, entry(id, "uid=u"+string(rune('a'+i))+",dc=example", attrs))
		}
		return answer{packets: append(out, done(id, code))}
	}
}

func fakeConnection(f *fake, set func(*accessapi.LDAPConnection)) coreldap.Connection {
	c := coreldap.Defaults("fake")
	c.URL = "ldap://" + f.addr + "/dc=example"
	c.SearchFilter, c.ManagerDN = "uid={0}", "cn=manager,dc=example"
	if set != nil {
		set(&c)
	}
	return coreldap.Connection{LDAPConnection: c, ManagerPassword: coreldap.SecretOf("manager-pw")}
}

// short bounds every wait, so the silent server's tests end quickly.
var short = accessldap.Client{ConnectTimeout: 300 * time.Millisecond, Timeout: 300 * time.Millisecond}

func directoryErr(t *testing.T, err error, op, reason string) {
	t.Helper()
	var failed *coreldap.DirectoryError
	if !errors.As(err, &failed) || failed.Op != op || !strings.Contains(failed.Reason, reason) {
		t.Fatalf("error %v, want %s: ...%s...", err, op, reason)
	}
	if strings.Contains(err.Error(), "manager-pw") || strings.Contains(err.Error(), "user-pw") {
		t.Fatalf("the error shows a password: %v", err)
	}
}

func TestUnitDirectoryFaults(t *testing.T) {
	ctx := t.Context()
	t.Run("malformed bind answer", func(t *testing.T) {
		f := startFake(t, func(ber.Tag, int64) answer { return answer{raw: []byte{0x30, 0x03, 0x02, 0x01}} })
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "manager bind", "")
	})
	t.Run("malformed search answer", func(t *testing.T) {
		f := startFake(t, func(op ber.Tag, id int64) answer {
			if op == goldap.ApplicationBindRequest {
				return bindOK(id)
			}
			return answer{raw: []byte{0x30, 0x84, 0xff, 0xff, 0xff, 0xff}}
		})
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "search", "")
	})
	t.Run("silent server times out", func(t *testing.T) {
		f := startFake(t, func(ber.Tag, int64) answer { return answer{hang: true} })
		start := time.Now()
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "manager bind", "timed out")
		if time.Since(start) > 5*time.Second {
			t.Fatalf("the timeout took %s", time.Since(start))
		}
	})
	t.Run("silent TLS handshake times out", func(t *testing.T) {
		// A listener that accepts and never reads: no handshake answer.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				t.Cleanup(func() { _ = conn.Close() })
			}
		}()
		c := fakeConnection(&fake{}, func(c *accessapi.LDAPConnection) { c.URL = "ldaps://" + ln.Addr().String() })
		_, err = short.Authenticate(ctx, c, "u", "user-pw")
		directoryErr(t, err, "tls", "deadline exceeded")
	})
	t.Run("cancelled while waiting", func(t *testing.T) {
		f := startFake(t, func(ber.Tag, int64) answer { return answer{hang: true} })
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err := accessldap.Client{}.Authenticate(cctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "manager bind", "deadline exceeded")
	})
	t.Run("manager bind unavailable", func(t *testing.T) {
		f := startFake(t, func(op ber.Tag, id int64) answer {
			return answer{packets: []*ber.Packet{result(id, goldap.ApplicationBindResponse,
				goldap.LDAPResultUnavailable, "busy")}}
		})
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "manager bind", "Unavailable")
	})
	t.Run("dn pattern bind unavailable", func(t *testing.T) {
		f := startFake(t, func(op ber.Tag, id int64) answer {
			return answer{packets: []*ber.Packet{result(id, goldap.ApplicationBindResponse,
				goldap.LDAPResultUnavailable, "busy")}}
		})
		c := fakeConnection(f, func(c *accessapi.LDAPConnection) { c.UserDNPattern = "uid={0}" })
		_, err := short.Authenticate(ctx, c, "u", "user-pw")
		directoryErr(t, err, "bind", "Unavailable")
	})
	t.Run("referral", func(t *testing.T) {
		f := startFake(t, searching(goldap.LDAPResultReferral))
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "search", "Referral")
	})
	t.Run("filter refused", func(t *testing.T) {
		f := startFake(t, searching(goldap.LDAPResultFilterError))
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "search", "refused search.search_filter")
	})
	t.Run("user bind unavailable after search", func(t *testing.T) {
		binds := 0
		f := startFake(t, func(op ber.Tag, id int64) answer {
			if op == goldap.ApplicationBindRequest {
				binds++
				if binds > 1 {
					return answer{packets: []*ber.Packet{result(id, goldap.ApplicationBindResponse,
						goldap.LDAPResultUnavailable, "")}}
				}
				return bindOK(id)
			}
			return answer{packets: []*ber.Packet{entry(id, "uid=u,dc=example", person), done(id, 0)}}
		})
		_, err := short.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw")
		directoryErr(t, err, "bind", "Unavailable")
	})
}

func TestUnitDirectoryResults(t *testing.T) {
	ctx := t.Context()
	java := map[string][]string{"objectClass": {"top", "javaNamingReference"}, "mail": {"u@example.org"}}
	planted := map[string][]string{"objectClass": {"inetOrgPerson"}, "javaFactory": {"Exploit"}}
	for _, tc := range []struct {
		name    string
		handle  func(ber.Tag, int64) answer
		set     func(*accessapi.LDAPConnection)
		want    error
		email   string
		paging  bool
		noPages bool
	}{
		{name: "one entry, paged", handle: searching(0, person), email: "u@example.org", paging: true},
		{
			name: "unpaged", handle: searching(0, person), email: "u@example.org", noPages: true,
			set: func(c *accessapi.LDAPConnection) { c.PagingSupportEnabled = false },
		},
		{name: "ambiguous", handle: searching(0, person, person), want: coreldap.ErrUnknownUser},
		{
			name: "size limit", handle: searching(goldap.LDAPResultSizeLimitExceeded, person),
			want: coreldap.ErrUnknownUser,
		},
		{name: "missing base", handle: searching(goldap.LDAPResultNoSuchObject), want: coreldap.ErrUnknownUser},
		{name: "java object refused", handle: searching(0, java), want: coreldap.ErrUnknownUser},
		{name: "java attribute refused", handle: searching(0, planted), want: coreldap.ErrUnknownUser},
		{
			name: "java object without protection", handle: searching(0, java), email: "u@example.org",
			set: func(c *accessapi.LDAPConnection) { c.PoisoningProtection = false },
		},
		{
			name: "dn pattern entry unreadable", handle: searching(goldap.LDAPResultInsufficientAccessRights),
			set: func(c *accessapi.LDAPConnection) { c.UserDNPattern, c.SearchFilter = "uid={0}", "" },
		},
		{
			name: "dn pattern java object refused", handle: searching(0, java), want: coreldap.ErrUnknownUser,
			set: func(c *accessapi.LDAPConnection) { c.UserDNPattern, c.SearchFilter = "uid={0}", "" },
		},
		{
			name: "dn pattern reads the email", handle: searching(0, person), email: "u@example.org",
			set: func(c *accessapi.LDAPConnection) { c.UserDNPattern, c.SearchFilter = "uid={0}", "" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFake(t, tc.handle)
			e, err := short.Authenticate(ctx, fakeConnection(f, tc.set), "u", "user-pw")
			if !errors.Is(err, tc.want) || e.Email != tc.email {
				t.Fatalf("Authenticate = %+v, %v; want %q, %v", e, err, tc.email, tc.want)
			}
			if tc.paging && !f.sawPaging() || tc.noPages && f.sawPaging() {
				t.Fatalf("paging control sent: %v", f.sawPaging())
			}
		})
	}
}

func TestUnitDirectoryURLs(t *testing.T) {
	ctx := t.Context()
	for _, raw := range []string{"http://h", "ldap://", "%zz"} {
		c := coreldap.Connection{LDAPConnection: accessapi.LDAPConnection{URL: raw, UserDNPattern: "uid={0}"}}
		_, err := short.Authenticate(ctx, c, "u", "pw")
		directoryErr(t, err, "connect", "ldap_url")
		directoryErr(t, short.Test(ctx, c), "connect", "ldap_url")
	}
	// Default ports: nothing listens on 389 or 636 of the discard prefix,
	// so the connect fails naming the port the URL left out.
	for raw, port := range map[string]string{"ldap://192.0.2.1": ":389", "ldaps://192.0.2.1": ":636"} {
		c := coreldap.Connection{LDAPConnection: accessapi.LDAPConnection{URL: raw, UserDNPattern: "uid={0}"}}
		_, err := accessldap.Client{ConnectTimeout: 50 * time.Millisecond}.Authenticate(ctx, c, "u", "pw")
		directoryErr(t, err, "connect", port)
	}
	if (accessldap.Client{}).ConnectTimeout != 0 || accessldap.DefaultTimeout != 15*time.Second ||
		accessldap.DefaultConnectTimeout != 10*time.Second {
		t.Fatal("JFrog's documented LDAP timeouts changed")
	}
}

func TestUnitDirectoryTestFaults(t *testing.T) {
	ctx := t.Context()
	t.Run("search base unreadable", func(t *testing.T) {
		f := startFake(t, searching(goldap.LDAPResultInsufficientAccessRights))
		directoryErr(t, short.Test(ctx, fakeConnection(f, nil)), "search", "Insufficient")
	})
	t.Run("search base found", func(t *testing.T) {
		f := startFake(t, searching(0, person))
		if err := short.Test(ctx, fakeConnection(f, nil)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("manager refused", func(t *testing.T) {
		f := startFake(t, func(_ ber.Tag, id int64) answer {
			return answer{packets: []*ber.Packet{result(id, goldap.ApplicationBindResponse,
				goldap.LDAPResultInvalidCredentials, "")}}
		})
		directoryErr(t, short.Test(ctx, fakeConnection(f, nil)), "manager bind", "refused")
	})
	t.Run("page size", func(t *testing.T) {
		f := startFake(t, searching(0, person))
		c := accessldap.Client{PageSize: 7, Timeout: time.Second}
		if _, err := c.Authenticate(ctx, fakeConnection(f, nil), "u", "user-pw"); err != nil {
			t.Fatal(err)
		}
	})
}

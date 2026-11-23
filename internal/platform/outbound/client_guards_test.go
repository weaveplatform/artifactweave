package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type refusedBody struct {
	closed bool
}

func (*refusedBody) Read([]byte) (int, error) { return 0, errors.New("secret response payload") }
func (b *refusedBody) Close() error           { b.closed = true; return errors.New("secret close payload") }

func TestUnitFixedOriginClientRefusesMissingPolicyAndUnsafeRequests(t *testing.T) {
	base := parsed(t, "https://target.example/path")
	policy, err := NewPolicy([]string{Origin(base)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := NewNTLMProxy("user", "secret", "CORP", "NODE")
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []ClientOptions{
		// An NTLM identity without a proxy would have nothing to authenticate.
		{Base: base, Policy: policy, NTLM: identity},
		{},
		{Base: base},
		{Base: base, Policy: policy, SocketTimeout: -time.Second},
		{Base: &url.URL{Scheme: "https", Host: "host:invalid"}, Policy: policy},
		{Base: base, Policy: policy, Proxy: parsed(t, "ftp://proxy.example")},
		{Base: base, Policy: policy, Proxy: parsed(t, "https://proxy.example/nested")},
	} {
		if client, err := NewClient(options); !errors.Is(err, ErrConfiguration) || client != nil {
			t.Fatal("unsafe client configuration accepted")
		}
	}
	client := testClient(t, base, nil, nil, time.Second, nil)
	if _, err := client.Do(nil); !errors.Is(err, ErrRequest) {
		t.Fatal("nil request accepted")
	}
	for _, target := range []*url.URL{nil, parsed(t, "https://other.example/path"), parsed(t, "https://user:secret@target.example/path")} {
		body := &refusedBody{}
		_, err := client.Do(&http.Request{URL: target, Body: body})
		if err == nil || !body.closed || strings.Contains(err.Error(), "secret") {
			t.Fatal("refusal leaked details or request ownership", err)
		}
	}
	if err := client.redirect(request(t, http.MethodGet, base.String(), ""), []*http.Request{{Method: http.MethodPost}}); !errors.Is(err, ErrRedirect) {
		t.Fatal("redirect callback trusted a converted write method")
	}
	if _, err := client.dial(t.Context(), "tcp", "unplanned.example:443"); !errors.Is(err, ErrSecurity) {
		t.Fatal("transport dial accepted an unchecked target")
	}
	client.Close()
	for _, send := range []func(*http.Request) (*http.Response, error){client.Do, checkedTransport{client}.RoundTrip} {
		body := &refusedBody{}
		_, err := send(&http.Request{URL: base, Body: body})
		if !errors.Is(err, ErrClosed) || !body.closed || strings.Contains(err.Error(), "secret") {
			t.Fatal("closed client leaked or retained a request", err)
		}
	}
}

func TestUnitFixedOriginBodyAndNetworkErrorsDiscardPrivateText(t *testing.T) {
	body := &refusedBody{}
	reader := safeBody{body}
	if _, err := io.ReadAll(reader); !errors.Is(err, ErrRequest) || strings.Contains(err.Error(), "secret") {
		t.Fatal("response body error leaked", err)
	}
	if err := reader.Close(); !errors.Is(err, ErrRequest) || !body.closed || strings.Contains(err.Error(), "secret") {
		t.Fatal("response close error leaked", err)
	}
	for original, want := range map[error]error{
		&net.OpError{Op: "read", Net: "secret", Err: os.ErrDeadlineExceeded}:                  context.DeadlineExceeded,
		&url.Error{Op: "PUT", URL: "https://secret.example/signed", Err: io.ErrUnexpectedEOF}: ErrConnection,
		errors.New("secret upstream body"):                                                    ErrRequest,
	} {
		err := SafeError(original)
		if !errors.Is(err, want) || strings.Contains(err.Error(), "secret") {
			t.Fatal("network failure leaked or lost classification", err)
		}
	}
}

func TestAccFixedOriginProxyRefusalsStaySeparateFromDestination(t *testing.T) {
	for _, status := range []int{http.StatusProxyAuthRequired, http.StatusForbidden, http.StatusBadGateway, 0} {
		name := http.StatusText(status)
		if status == 0 {
			name = "untrusted proxy TLS"
		}
		t.Run(name, func(t *testing.T) {
			routes := chi.NewRouter()
			routes.Method(http.MethodConnect, "/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status == 0 {
					w.WriteHeader(http.StatusNoContent)
				} else {
					w.WriteHeader(status)
				}
				_, _ = io.WriteString(w, "secret proxy refusal")
			}))
			var proxy *httptest.Server
			if status == 0 {
				proxy = httptest.NewTLSServer(routes)
			} else {
				proxy = httptest.NewServer(routes)
			}
			t.Cleanup(proxy.Close)
			base := parsed(t, "https://destination.example/path")
			client := testClient(t, base, parsed(t, proxy.URL), nil, time.Second, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			})
			_, err := client.Do(request(t, http.MethodPut, base.String(), "private artifact"))
			want := ErrSecurity
			if status == http.StatusBadGateway {
				want = ErrConnection
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "secret") {
				t.Fatal("proxy refusal became an origin response or leaked details", err)
			}
		})
	}
}

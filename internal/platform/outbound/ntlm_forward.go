package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
)

// ForwardTransport decorates a fresh, caller-configured HTTP proxy transport.
// The caller owns proxy TLS, checked physical URLs, origin credentials and pool
// lifetime. It must remove proxy URL userinfo: Basic fallback is never used.
// One connection per transport keeps each bounded 407 exchange on its own socket;
// a closed connection cannot receive another socket's authenticate message.
// Requests with bodies authenticate using HEAD first, then send the original body
// once, preserving non-replayable Smart Remote writes. The origin sees the probe
// only after authentication. The returned RoundTripper is safe for concurrent use.
func (identity *NTLMProxy) ForwardTransport(tr *http.Transport) http.RoundTripper {
	f := &ntlmForward{identity: identity, transport: tr, gate: make(chan struct{}, 1)}
	tr.MaxConnsPerHost = 1
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if f.negotiating.Load() {
			return nil, ErrSecurity
		}
		return dial(ctx, network, address)
	}
	if secureDial := tr.DialTLSContext; secureDial != nil {
		tr.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if f.negotiating.Load() {
				return nil, ErrSecurity
			}
			return secureDial(ctx, network, address)
		}
	}
	return f
}

type ntlmForward struct {
	identity    *NTLMProxy
	transport   *http.Transport
	gate        chan struct{}
	negotiating atomic.Bool
}

func (f *ntlmForward) RoundTrip(original *http.Request) (response *http.Response, err error) {
	select {
	case f.gate <- struct{}{}:
	case <-original.Context().Done():
		if original.Body != nil {
			_ = original.Body.Close()
		}
		return nil, SafeError(original.Context().Err())
	}
	defer func() { f.negotiating.Store(false); <-f.gate }()
	defer func() {
		if err != nil {
			f.transport.CloseIdleConnections()
			if original.Body != nil {
				_ = original.Body.Close()
			}
		}
	}()
	request := original.Clone(original.Context())
	request.Header.Del("Proxy-Authorization")
	probe := request.Body != nil && request.Body != http.NoBody
	if probe {
		request.Method = http.MethodHead
		request.Body = nil
		request.GetBody = nil
		request.ContentLength = 0
		request.TransferEncoding = nil
		request.Header.Del("Content-Length")
		request.Header.Del("Transfer-Encoding")
		request.Header.Del("Expect")
	}
	for stage := range 3 {
		response, err = f.transport.RoundTrip(request)
		if err != nil {
			return nil, SafeError(err)
		}
		if response.StatusCode != http.StatusProxyAuthRequired {
			if !probe {
				return response, nil
			}
			if err = drainNTLMResponse(response); err != nil {
				return nil, err
			}
			request = original.Clone(original.Context())
			request.Header.Del("Proxy-Authorization")
			// The original body is never a negotiation message and is never replayed.
			request.GetBody = nil
			f.negotiating.Store(true)
			response, err = f.transport.RoundTrip(request)
			if err != nil {
				return nil, SafeError(err)
			}
			if response.StatusCode == http.StatusProxyAuthRequired {
				_ = response.Body.Close()
				return nil, ErrSecurity
			}
			return response, nil
		}
		if stage == 2 || response.Close {
			_ = response.Body.Close()
			return nil, ErrSecurity
		}
		f.negotiating.Store(true)
		authorization, authErr := f.identity.authorization(response.Header, stage)
		if authErr != nil {
			_ = response.Body.Close()
			return nil, authErr
		}
		if err = drainNTLMResponse(response); err != nil {
			return nil, err
		}
		request = request.Clone(original.Context())
		request.Header.Set("Proxy-Authorization", authorization)
	}
	return nil, ErrSecurity
}

func drainNTLMResponse(response *http.Response) error {
	n, err := io.CopyN(io.Discard, response.Body, 64<<10+1)
	closeErr := response.Body.Close()
	if n > 64<<10 || !errors.Is(err, io.EOF) || closeErr != nil {
		return ErrSecurity
	}
	return nil
}

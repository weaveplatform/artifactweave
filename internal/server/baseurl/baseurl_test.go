package baseurl

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnitResolvePrecedence(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		configured string
		tls        bool
		want       string
	}{
		{
			"override wins over everything",
			map[string]string{
				OverrideHeader:     "https://art.example.com/artifactory/",
				"X-Forwarded-Host": "proxy",
			},
			"http://cfg",
			false,
			"https://art.example.com/artifactory",
		},
		{
			"override trims whitespace",
			map[string]string{OverrideHeader: "  https://x.example  "},
			"",
			false,
			"https://x.example",
		},
		{
			"a configured base url wins over every forwarded header",
			map[string]string{
				"X-Forwarded-Host":  "evil.example",
				"X-Forwarded-Proto": "http",
				"X-Forwarded-Port":  "9999",
			},
			"https://art.example.com/",
			true,
			"https://art.example.com",
		},
		{
			"the override wins over a configured base url",
			map[string]string{
				OverrideHeader:     "https://edge.example.com",
				"X-Forwarded-Host": "evil.example",
			},
			"https://art.example.com",
			false,
			"https://edge.example.com",
		},
		{
			"forwarded host and proto, with no configured base url",
			map[string]string{
				"X-Forwarded-Host":  "proxy.example.com",
				"X-Forwarded-Proto": "https",
			},
			"",
			false,
			"https://proxy.example.com",
		},
		{
			"forwarded host without proto uses request scheme",
			map[string]string{"X-Forwarded-Host": "proxy.example.com"},
			"",
			false,
			"http://proxy.example.com",
		},
		{
			"forwarded port appended",
			map[string]string{
				"X-Forwarded-Host":  "proxy",
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Port":  "8443",
			},
			"",
			false,
			"https://proxy:8443",
		},
		{
			"forwarded default port dropped",
			map[string]string{
				"X-Forwarded-Host":  "proxy",
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Port":  "443",
			},
			"",
			false,
			"https://proxy",
		},
		{
			"forwarded host already has port",
			map[string]string{"X-Forwarded-Host": "proxy:9000", "X-Forwarded-Port": "8443"},
			"",
			false,
			"http://proxy:9000",
		},
		{
			"a proxy chain's hosts take the first, client-facing one",
			map[string]string{
				"X-Forwarded-Host":  "a.example, b.internal",
				"X-Forwarded-Proto": "https, http",
				"X-Forwarded-Port":  "443, 8080",
			},
			"",
			false,
			"https://a.example",
		},
		{
			"whitespace around a list's entries is trimmed",
			map[string]string{
				"X-Forwarded-Host":  "  a.example ,b.internal",
				"X-Forwarded-Proto": " https ,http",
				"X-Forwarded-Port":  " 8443 , 80",
			},
			"",
			false,
			"https://a.example:8443",
		},
		{
			"an empty first host with a configured base url: the configured one",
			map[string]string{"X-Forwarded-Host": " , b.internal"},
			"https://cfg.example.com",
			false,
			"https://cfg.example.com",
		},
		{
			"an empty first host and no configuration: the request",
			map[string]string{"X-Forwarded-Host": ",b.internal"},
			"",
			false,
			"http://origin.test:8081",
		},
		{
			"an IPv6 host with its port, in a list",
			map[string]string{
				"X-Forwarded-Host": "[::1]:8443, b.internal",
				"X-Forwarded-Port": "9000",
			},
			"",
			false,
			"http://[::1]:8443",
		},
		{
			"an IPv6 host without a port takes the forwarded port",
			map[string]string{
				"X-Forwarded-Host":  "[2001:db8::1], b.internal",
				"X-Forwarded-Proto": "https",
				"X-Forwarded-Port":  "8443, 80",
			},
			"",
			false,
			"https://[2001:db8::1]:8443",
		},
		{
			"an unknown forwarded proto falls back to the request's",
			map[string]string{"X-Forwarded-Host": "proxy", "X-Forwarded-Proto": "gopher"},
			"",
			true,
			"https://proxy",
		},
		{
			"a forwarded proto in capitals",
			map[string]string{"X-Forwarded-Host": "proxy", "X-Forwarded-Proto": "HTTPS"},
			"",
			false,
			"https://proxy",
		},
		{
			"a forwarded port that is not a number is ignored",
			map[string]string{"X-Forwarded-Host": "proxy", "X-Forwarded-Port": "eighty"},
			"",
			false,
			"http://proxy",
		},
		{"configured base url", nil, "https://cfg.example.com/", false, "https://cfg.example.com"},
		{"request host http", nil, "", false, "http://origin.test:8081"},
		{"request host https", nil, "", true, "https://origin.test:8081"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(
				http.MethodGet,
				"http://origin.test:8081/artifactory/api/x",
				nil,
			)
			r.Host = "origin.test:8081"
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := Resolve(r, tc.configured); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAccMiddlewareAndFrom(t *testing.T) {
	var seen string
	h := Middleware(
		"https://cfg.example.com",
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = From(r.Context())
		}),
	)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if seen != "https://cfg.example.com" {
		t.Fatalf("From returned %q", seen)
	}
	if From(context.Background()) != "" {
		t.Fatal("From on a bare context should be empty")
	}
}

func TestAccForwardedHost(t *testing.T) {
	for header, want := range map[string]string{
		"":                       "",
		"a.example":              "a.example",
		"a.example, b.internal":  "a.example",
		" a.example ,b.internal": "a.example",
		", b.internal":           "",
		"[::1]:8443, b.internal": "[::1]:8443",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("X-Forwarded-Host", header)
		}
		if got := ForwardedHost(r); got != want {
			t.Errorf("ForwardedHost(%q) = %q, want %q", header, got, want)
		}
	}
}

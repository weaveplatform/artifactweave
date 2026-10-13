package baseurl

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
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
			"forwarded host and proto",
			map[string]string{
				"X-Forwarded-Host":  "proxy.example.com",
				"X-Forwarded-Proto": "https",
			},
			"http://cfg",
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

func TestMiddlewareAndFrom(t *testing.T) {
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

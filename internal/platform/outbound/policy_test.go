package outbound

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
)

func parsed(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUnitDestinationPolicyRetainsPrivateAndMetadataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		url, ip string
		private bool
		refused bool
	}{
		{"https://target.example/item", "8.8.8.8", false, false},
		{"https://target.example/item", "2606:4700::1111", false, false},
		{"https://target.example/item", "10.0.0.1", false, true},
		{"https://target.example/item", "10.0.0.1", true, false},
		{"http://127.0.0.1/item", "", false, true},
		{"http://127.0.0.1/item", "", true, false},
		{"http://[::ffff:127.0.0.1]/item", "", false, true},
		{"http://169.254.169.254/token", "", true, true},
		{"http://100.100.100.200/token", "", true, true},
		{"http://168.63.129.16/token", "", true, true},
		{"http://[fd00:ec2::254]/token", "", true, true},
		{"http://metadata.google.internal/token", "8.8.8.8", true, true},
		{"http://224.0.0.1/item", "", true, true},
		{"http://0.0.0.0/item", "", true, true},
		{"https://target.example/item", "fd00::1", false, true},
		{"https://target.example/item", "100.64.0.1", false, true},
		{"https://target.example/item", "203.0.113.1", false, true},
		{"https://target.example/item", "64:ff9b::a00:1", false, true},
		{"https://target.example/item", "2002:a00:1::", false, true},
		{"https://reader:secret@target.example/item", "8.8.8.8", false, true},
	} {
		t.Run(tc.url+" "+tc.ip, func(t *testing.T) {
			u := parsed(t, tc.url)
			var private []string
			if tc.private {
				private = []string{Origin(u)}
			}
			policy, err := NewPolicy(private, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(tc.ip)}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			addresses, err := policy.Addresses(t.Context(), u)
			if tc.refused && !errors.Is(err, ErrSecurity) || !tc.refused && (err != nil || len(addresses) != 1) {
				t.Fatal("destination boundary changed", addresses, err)
			}
		})
	}
}

func TestUnitDestinationPolicyRejectsMixedDNSAndRedactsLookupErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []net.IPAddr
		err  error
		want error
	}{
		{name: "mixed", ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, want: ErrSecurity},
		{name: "invalid", ips: []net.IPAddr{{IP: []byte{1, 2}}}, want: ErrSecurity},
		{name: "zone", ips: []net.IPAddr{{IP: net.ParseIP("2606:4700::1111"), Zone: "en0"}}, want: ErrSecurity},
		{name: "empty", want: ErrConnection},
		{name: "lookup failure", err: &net.DNSError{Err: "secret lookup payload", Name: "secret.example"}, want: ErrConnection},
		{name: "cancelled", err: context.Canceled, want: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewPolicy(nil, func(context.Context, string) ([]net.IPAddr, error) { return tc.ips, tc.err })
			if err != nil {
				t.Fatal(err)
			}
			_, err = policy.Addresses(t.Context(), parsed(t, "https://target.example/item"))
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe lookup classification", err)
			}
		})
	}
	for _, raw := range []string{"ftp://host", "https://reader:secret@host", "https://host:0", "https://host:65536", "https://host/#fragment", "https://[fe80::1%25en0]/"} {
		if _, err := NewPolicy([]string{raw}, nil); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid configured authority accepted")
		}
	}
}

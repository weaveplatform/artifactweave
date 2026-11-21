package platform

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestUnitCloudFrontWaitsForRunnerDNSBeforeChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ready := scenarioReady["aws/cdn"]
		if ready == nil {
			t.Fatal("aws/cdn has no DNS readiness guard before its download checks")
		}
		calls := 0
		const host = "cdn.fixture.invalid"
		h := &harness{
			storageHosts: map[string]bool{host: true},
			dnsLookup: func(_ context.Context, name string) ([]string, error) {
				calls++
				if name != host {
					t.Fatalf("lookup host = %q, want the scenario CDN host", name)
				}
				if calls < 3 {
					return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
				}
				return []string{"192.0.2.1"}, nil
			},
		}
		started := time.Now()
		ready(t, h)
		if calls != 3 || time.Since(started) != 10*time.Second {
			t.Fatalf("DNS readiness made %d lookups over %s, want three across two polling intervals", calls, time.Since(started))
		}
	})
}

func TestUnitDNSReadinessRequiresAddressesBeforeDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookup func(context.Context, string) ([]string, error)
	}{
		{"NXDOMAIN persists", func(_ context.Context, host string) ([]string, error) {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}},
		{"empty answer", func(context.Context, string) ([]string, error) { return nil, nil }},
		{"lookup waits for cancellation", func(ctx context.Context, _ string) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				started := time.Now()
				err := waitForHostDNS(ctx, "cdn.fixture.invalid", tc.lookup, 5*time.Second)
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 15*time.Second {
					t.Fatalf("DNS wait ended after %s with %v, want deadline failure", time.Since(started), err)
				}
				if tc.name == "NXDOMAIN persists" {
					var dnsError *net.DNSError
					if !errors.As(err, &dnsError) || !dnsError.IsNotFound {
						t.Fatalf("DNS failure lost the original NXDOMAIN: %v", err)
					}
				}
			})
		})
	}
}

func TestUnitDNSReadinessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	err := waitForHostDNS(ctx, "cdn.fixture.invalid", func(context.Context, string) ([]string, error) {
		calls++
		return []string{"192.0.2.1"}, nil
	}, time.Second)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled wait made %d lookups and returned %v", calls, err)
	}
}

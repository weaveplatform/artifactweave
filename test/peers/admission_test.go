package peers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"
)

// Admission must tolerate the finite writer delays induced by instrumented
// metadata work, while a backend that stays blocked still answers 503.
func TestAccPeerFixtureAdmissionWithMetadataWriter(t *testing.T) {
	cluster := Start(t, Options{Instances: 1, Driver: SQLite})
	instance := cluster.Instances[0]
	store := instance.openStore(t)
	for _, tc := range []struct {
		name         string
		releaseEarly bool
	}{
		{"finite_writer", true}, {"blocked_writer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locked := make(chan struct{})
			release := make(chan struct{})
			writerDone := make(chan struct{})
			var releaseOnce sync.Once
			unlock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unlock()
			var writerErr error
			go func() {
				writerErr = store.Tx(t.Context(), func(context.Context) error {
					close(locked)
					<-release
					return nil
				})
				close(writerDone)
			}()
			t.Cleanup(func() {
				unlock()
				select {
				case <-writerDone:
					if writerErr != nil && !errors.Is(writerErr, context.Canceled) {
						t.Errorf("writer: %v", writerErr)
					}
				case <-time.After(5 * time.Second):
					t.Error("metadata writer did not finish")
				}
			})
			select {
			case <-locked:
			case <-writerDone:
				t.Fatalf("writer could not acquire metadata: %v", writerErr)
			case <-time.After(5 * time.Second):
				t.Fatal("writer never acquired metadata")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			wrote := make(chan struct{})
			var wroteOnce sync.Once
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
				wroteOnce.Do(func() { close(wrote) })
			}})
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, instance.URL+"/artifactory/api/repositories", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.SetBasicAuth(AdminUser, instance.AdminPassword)
			type result struct {
				status int
				body   string
				err    error
			}
			responses := make(chan result, 1)
			go func() {
				response, err := cluster.client.Do(request)
				if err != nil {
					responses <- result{err: err}
					return
				}
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				responses <- result{status: response.StatusCode, body: string(body), err: errors.Join(readErr, closeErr)}
			}()
			select {
			case <-wrote:
			case got := <-responses:
				t.Fatalf("request did not reach fixture: %+v", got)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if tc.releaseEarly {
				select {
				case got := <-responses:
					t.Fatalf("admission returned while finite metadata work held the writer: %+v", got)
				case <-time.After(time.Second):
				}
				unlock()
			}
			var got result
			select {
			case got = <-responses:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			unlock()
			select {
			case <-writerDone:
				if writerErr != nil {
					t.Fatal(writerErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			expected := http.StatusServiceUnavailable
			if tc.releaseEarly {
				expected = http.StatusOK
			}
			if got.err != nil || got.status != expected {
				t.Fatalf("admission=%d %s (%v), want %d", got.status, got.body, got.err, expected)
			}
			if !tc.releaseEarly && !strings.Contains(got.body, "API admission is temporarily unavailable") {
				t.Fatalf("unavailable answer: %s", got.body)
			}
		})
	}
}

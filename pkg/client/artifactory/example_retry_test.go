package artifactory_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	artifactory "github.com/weaveplatform/artifactweave/pkg/client/artifactory"
)

// This example deliberately protects only build append. The generated client
// does not infer retry safety from an HTTP method or retry every failed write.
func ExampleClient_AppendBuildWithBody() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := artifactory.NewClient(
		"https://registry.example/artifactory",
		artifactory.WithHTTPClient(&http.Client{
			Timeout:       35 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}),
	)
	if err != nil {
		return
	}
	// Supply the same verified credential with a RequestEditorFn on client
	// construction. Persist these intent bytes and this unique key before the
	// first attempt if recovery must survive the caller's own process restart.
	key := `"8e03978e-40d5-43e8-bc93-6894a57f9324"`
	params := artifactory.AppendBuildParams{IdempotencyKey: &key}
	// Marshal once: semantically equivalent JSON with different bytes conflicts.
	body := []byte(`[{"id":"compiled-module"}]`)
	_ = appendWithRetry(ctx, client, "app", "42", params, body, waitRetry)
	// On failure, retain the same key/body/credential and reconcile the run.
	// Never create a new key merely because an earlier attempt timed out.
}

func appendWithRetry(
	ctx context.Context,
	client *artifactory.Client,
	name, number string,
	params artifactory.AppendBuildParams,
	body []byte,
	wait func(context.Context, time.Duration) error,
) error {
	if params.IdempotencyKey == nil || *params.IdempotencyKey == "" {
		return errors.New("protected append needs a persisted idempotency key")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var last error
	for attempt := range 5 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("append deadline: %w", err)
		}
		response, err := client.AppendBuildWithBody(ctx, name, number, &params,
			"application/json", bytes.NewReader(body))
		var retryAfter time.Duration
		retry := err != nil
		last = err
		if response != nil {
			// Bounded drain; http.Client.Timeout and ctx bound reads as well.
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusNoContent {
				return nil
			}
			if err == nil {
				last = fmt.Errorf("append returned HTTP %d", response.StatusCode)
				switch response.StatusCode {
				case 429, 500, 502, 503, 504:
					retry = true
				}
			}
			retryAfter = retryDelay(response.Header.Get("Retry-After"), time.Now())
		}
		if !retry || attempt == 4 {
			break
		}
		// Full jitter plus a server lower bound. Never shorten Retry-After to
		// fit the deadline; fail with the same unresolved intent instead.
		delay := max(
			retryAfter,
			time.Duration(rand.Int64N(int64(min(5*time.Second, 200*time.Millisecond<<attempt)))),
		)
		if err := wait(ctx, delay); err != nil {
			return fmt.Errorf("append retry: %w", err)
		}
	}
	return fmt.Errorf("append incomplete; reconcile using the original key: %w", last)
}

func retryDelay(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		// Anything beyond the overall deadline need not be represented exactly.
		return time.Duration(min(seconds, 91)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(91*time.Second, max(0, at.Sub(now)))
	}
	return 0
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type retryExampleDoer func(*http.Request) (*http.Response, error)

func (f retryExampleDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestUnitProtectedAppendRetryExample(t *testing.T) {
	body := []byte(`[{"id":"once"}]`)
	key := `"persisted-intent"`
	for _, scenario := range []string{"response loss", "throttled", "mismatch", "attempt limit", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			attempts, waits := 0, 0
			client, err := artifactory.NewClient(
				"https://registry.example/artifactory",
				artifactory.WithHTTPClient(
					retryExampleDoer(func(r *http.Request) (*http.Response, error) {
						attempts++
						got, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Equal(got, body) ||
							r.Header.Get("Idempotency-Key") != key {
							t.Fatalf("intent changed on attempt %d", attempts)
						}
						if scenario == "response loss" && attempts == 1 {
							return nil, io.ErrUnexpectedEOF
						}
						status := 204
						if scenario == "mismatch" {
							status = 422
						}
						if scenario == "attempt limit" || scenario == "deadline" ||
							scenario == "throttled" && attempts == 1 {
							status = 503
						}
						return &http.Response{
							StatusCode: status,
							Header:     http.Header{"Retry-After": {"2"}},
							Body:       io.NopCloser(strings.NewReader("")),
						}, nil
					}),
				),
			)
			if err != nil {
				t.Fatal(err)
			}
			err = appendWithRetry(
				t.Context(),
				client,
				"app",
				"1",
				artifactory.AppendBuildParams{IdempotencyKey: &key},
				body,
				func(_ context.Context, delay time.Duration) error {
					waits++
					if scenario != "response loss" && delay < 2*time.Second {
						t.Fatal("ignored Retry-After")
					}
					if scenario == "deadline" {
						return context.DeadlineExceeded
					}
					return nil
				},
			)
			if scenario == "response loss" || scenario == "throttled" {
				if err != nil || attempts != 2 || waits != 1 {
					t.Fatalf("recovery: attempts=%d waits=%d err=%v", attempts, waits, err)
				}
			} else if err == nil {
				t.Fatal("unsafe/incomplete request reported success")
			}
			if scenario == "mismatch" && attempts != 1 ||
				scenario == "attempt limit" && attempts != 5 ||
				scenario == "deadline" && attempts != 1 {
				t.Fatalf("unbounded/unsafe retry: %d", attempts)
			}
		})
	}
}

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

// These examples protect only operations whose API description declares
// Idempotency-Key: build append and promotion here, build delete alike. The
// generated client does not infer retry safety from an HTTP method or retry
// every failed write.
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

// ExampleClient_PromoteBuildWithBody protects a promotion as the append is
// protected: one key and one marshalled body per intent, the same on every
// attempt. A replay answers 200 with the original promotion's messages,
// although its files have since moved.
func ExampleClient_PromoteBuildWithBody() {
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
	key := `"0b6f2a9c-7d41-4c2e-9a53-2f8e1d4c6b70"`
	params := artifactory.PromoteBuildParams{IdempotencyKey: &key}
	body := []byte(`{"status":"released","targetRepo":"libs-release-local"}`)
	_ = promoteWithRetry(ctx, client, "app", "42", params, body, waitRetry)
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
	return retryProtected(ctx, "append", http.StatusNoContent,
		func(ctx context.Context) (*http.Response, error) {
			return client.AppendBuildWithBody(ctx, name, number, &params,
				"application/json", bytes.NewReader(body))
		}, wait)
}

func promoteWithRetry(
	ctx context.Context,
	client *artifactory.Client,
	name, number string,
	params artifactory.PromoteBuildParams,
	body []byte,
	wait func(context.Context, time.Duration) error,
) error {
	if params.IdempotencyKey == nil || *params.IdempotencyKey == "" {
		return errors.New("protected promotion needs a persisted idempotency key")
	}
	return retryProtected(ctx, "promotion", http.StatusOK,
		func(ctx context.Context) (*http.Response, error) {
			return client.PromoteBuildWithBody(ctx, name, number, &params,
				"application/json", bytes.NewReader(body))
		}, wait)
}

// retryProtected sends a keyed request until it answers success or a
// status that a retry cannot change. send makes a new body reader over the
// same bytes on every attempt.
func retryProtected(
	ctx context.Context,
	action string,
	success int,
	send func(context.Context) (*http.Response, error),
	wait func(context.Context, time.Duration) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var last error
	for attempt := range 5 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s deadline: %w", action, err)
		}
		response, err := send(ctx)
		var retryAfter time.Duration
		retry := err != nil
		last = err
		if response != nil {
			// Bounded drain; http.Client.Timeout and ctx bound reads as well.
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			if err == nil && response.StatusCode == success {
				return nil
			}
			if err == nil {
				last = fmt.Errorf("%s returned HTTP %d", action, response.StatusCode)
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
			return fmt.Errorf("%s retry: %w", action, err)
		}
	}
	return fmt.Errorf("%s incomplete; reconcile using the original key: %w", action, last)
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

// The promotion example sends the same key and bytes after a lost response
// and takes the 200 a replay answers; it refuses to start without a key.
func TestUnitProtectedPromoteRetryExample(t *testing.T) {
	body := []byte(`{"status":"released"}`)
	key := `"persisted-promotion"`
	attempts := 0
	client, err := artifactory.NewClient(
		"https://registry.example/artifactory",
		artifactory.WithHTTPClient(
			retryExampleDoer(func(r *http.Request) (*http.Response, error) {
				attempts++
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, body) || r.Header.Get("Idempotency-Key") != key {
					t.Fatalf("intent changed on attempt %d", attempts)
				}
				if attempts == 1 {
					return nil, io.ErrUnexpectedEOF
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(`{"messages":[]}`)),
				}, nil
			}),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	noWait := func(context.Context, time.Duration) error { return nil }
	params := artifactory.PromoteBuildParams{IdempotencyKey: &key}
	if err := promoteWithRetry(t.Context(), client, "app", "1", params, body, noWait); err != nil ||
		attempts != 2 {
		t.Fatalf("recovery: attempts=%d err=%v", attempts, err)
	}
	if err := promoteWithRetry(
		t.Context(), client, "app", "1", artifactory.PromoteBuildParams{}, body, noWait,
	); err == nil {
		t.Fatal("a promotion without a key was retried")
	}
}

package containertest

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// never is a check that does not return until the test ends: an engine
// that accepted the connection and never answered, called without a
// context as testcontainers calls it.
func never(t *testing.T) func(context.Context) error {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func(context.Context) error {
		<-release
		return nil
	}
}

func TestAccWithDeadline(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name  string
		check func(context.Context) error
		// want is the error expected, or nil; fails asks only for one.
		want  error
		fails bool
	}{
		{"answers", func(context.Context) error { return nil }, nil, false},
		{"refuses", func(context.Context) error { return boom }, boom, true},
		{"never answers", never(t), errNoAnswer, true},
		{"panics", func(context.Context) error { panic("no Docker host") }, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const timeout = 100 * time.Millisecond
			start := time.Now()
			err := withDeadline(context.Background(), timeout, tc.check)
			took := time.Since(start)
			if (err != nil) != tc.fails || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v (fails %t)", err, tc.want, tc.fails)
			}
			// The bound is generous for a slow runner, and far short of
			// the hang the deadline exists to stop.
			if took > timeout+2*time.Second {
				t.Fatalf("took %s with a %s deadline", took, timeout)
			}
			if errors.Is(err, errNoAnswer) && took < timeout {
				t.Fatalf("gave up after %s, before the %s deadline", took, timeout)
			}
		})
	}
}

// TestAccSkipUnlessSkipsOnSilentEngine: a test whose engine never answers is
// skipped, not failed and not left waiting.
func TestAccSkipUnlessSkipsOnSilentEngine(t *testing.T) {
	check := never(t)
	var sub *testing.T
	t.Run("docker-backed", func(t *testing.T) {
		sub = t
		skipUnless(t, func(ctx context.Context) error {
			return withDeadline(ctx, 100*time.Millisecond, check)
		})
		t.Fatal("ran without an engine")
	})
	if !sub.Skipped() {
		t.Fatal("the Docker-backed test was not skipped")
	}
}

// TestAccLinuxEngineBoundedOnSilentSocket points testcontainers at a socket
// that accepts connections and never answers, the state #458 found Docker
// Desktop in, and requires the real check to give up at the deadline.
func TestAccLinuxEngineBoundedOnSilentSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	// An empty home hides any ~/.testcontainers.properties, whose tc.host
	// would win over DOCKER_HOST.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DOCKER_HOST", "tcp://"+ln.Addr().String())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CONTEXT", "")

	const timeout = 500 * time.Millisecond
	start := time.Now()
	err = withDeadline(context.Background(), timeout, linuxEngine)
	if !errors.Is(err, errNoAnswer) {
		t.Fatalf("error %v, want %v", err, errNoAnswer)
	}
	if took := time.Since(start); took > timeout+2*time.Second {
		t.Fatalf("took %s with a %s deadline", took, timeout)
	}
}

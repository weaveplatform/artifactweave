package containertest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/client"
)

// testImage is a digest reference like the emulator constants.
const testImage = "registry.test/emulator@sha256:" +
	"0000000000000000000000000000000000000000000000000000000000000001"

// pullProgress is the start of every pull stream: the engine has answered
// 200 before it fetches anything.
const pullProgress = `{"status":"Pulling from emulator","id":"sha256"}` + "\n" +
	`{"status":"Pulling fs layer","progressDetail":{},"id":"0123456789ab"}` + "\n"

// fakeEngine models the two Docker Engine API routes ensureImage calls:
// image inspect, which finds the image only once a pull has stored it, and
// image create (a pull), which answers 200 and streams pullBody.
type fakeEngine struct {
	// pullStatus, when not 0, refuses the pull with that status before any
	// stream, as the engine does for a reference it cannot resolve.
	pullStatus int
	pullBody   string
	// stores says whether a pull that answered 200 leaves the image there.
	stores bool
	// hold, when set, delays each pull's answer until it is closed.
	hold chan struct{}

	mu      sync.Mutex
	present bool
	pulls   int
	auth    []string
}

func (e *fakeEngine) start(t *testing.T) imageAPI {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/{version}/images/*", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		present := e.present
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !present {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"message":"No such image: %s"}`,
				strings.TrimSuffix(chi.URLParam(r, "*"), "/json"))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"sha256:1"}`))
	})
	r.Post("/{version}/images/create", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.pulls++
		e.auth = append(e.auth, r.Header.Get("X-Registry-Auth"))
		e.mu.Unlock()
		if e.hold != nil {
			<-e.hold
		}
		w.Header().Set("Content-Type", "application/json")
		if e.pullStatus != 0 {
			w.WriteHeader(e.pullStatus)
			_, _ = w.Write([]byte(`{"message":"pull refused"}`))
			return
		}
		_, _ = w.Write([]byte(e.pullBody))
		if e.stores {
			e.mu.Lock()
			e.present = true
			e.mu.Unlock()
		}
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	cli, err := client.New(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithAPIVersion("1.54"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func (e *fakeEngine) counts() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pulls, append([]string(nil), e.auth...)
}

// TestUnitEnsureImage covers each answer a pull can give. The stream cases
// are #1448: testcontainers-go drains the stream without decoding it, so it
// took them for a finished pull and the container create then failed with
// "No such image".
func TestUnitEnsureImage(t *testing.T) {
	cases := []struct {
		name    string
		engine  *fakeEngine
		pulls   int
		wantErr string
	}{
		{
			name:   "present already",
			engine: &fakeEngine{present: true},
		},
		{
			name: "pulled",
			engine: &fakeEngine{
				pullBody: pullProgress + `{"status":"Status: Downloaded newer image"}` + "\n",
				stores:   true,
			},
			pulls: 1,
		},
		{
			name: "registry error inside the stream",
			engine: &fakeEngine{pullBody: pullProgress +
				`{"errorDetail":{"code":502,"message":"received unexpected HTTP status: 502 Bad Gateway"},` +
				`"error":"received unexpected HTTP status: 502 Bad Gateway"}` + "\n"},
			pulls:   1,
			wantErr: "pull " + testImage + ": received unexpected HTTP status: 502 Bad Gateway",
		},
		{
			name: "error inside the stream without a code",
			engine: &fakeEngine{pullBody: pullProgress +
				`{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}` + "\n"},
			pulls:   1,
			wantErr: "pull " + testImage + ": unexpected EOF",
		},
		{
			name:    "stream ends cleanly without the image",
			engine:  &fakeEngine{pullBody: pullProgress},
			pulls:   1,
			wantErr: "pull " + testImage + " ended without an error, but the engine does not have it",
		},
		{
			name:    "pull refused",
			engine:  &fakeEngine{pullStatus: http.StatusInternalServerError},
			pulls:   1,
			wantErr: "pull " + testImage + ": ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := tc.engine.start(t)
			err := ensureImage(context.Background(), api, testImage, "credentials")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("error %v, want none", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
			}
			pulls, auth := tc.engine.counts()
			if pulls != tc.pulls {
				t.Fatalf("%d pulls, want %d", pulls, tc.pulls)
			}
			for _, a := range auth {
				if a != "credentials" {
					t.Fatalf("pull sent X-Registry-Auth %q, want the caller's", a)
				}
			}
		})
	}
}

// TestUnitEnsureImageOnePullAtATime starts one emulator image from several
// callers at once, as tests that start emulators in parallel do. The first
// pull is held open while the others arrive; they wait for it and then find
// the image, so the engine sees one pull.
func TestUnitEnsureImageOnePullAtATime(t *testing.T) {
	engine := &fakeEngine{
		pullBody: pullProgress + `{"status":"Status: Downloaded newer image"}` + "\n",
		stores:   true,
		hold:     make(chan struct{}),
	}
	api := engine.start(t)
	const callers = 8
	var started, done sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		started.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			started.Done()
			errs <- ensureImage(context.Background(), api, testImage, "")
		}()
	}
	started.Wait()
	// Without the lock, every caller would inspect, miss the image and
	// pull while the first pull is held; the hold gives them that time.
	time.Sleep(200 * time.Millisecond)
	close(engine.hold)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if pulls, _ := engine.counts(); pulls != 1 {
		t.Fatalf("%d pulls, want 1", pulls)
	}
}

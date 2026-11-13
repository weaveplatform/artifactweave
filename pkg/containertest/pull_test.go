package containertest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/client"
)

// testDigest stands for an emulator's pinned digest; primary and mirror
// serve it as an emulator's first and second source do.
const (
	testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	primary    = "primary.test/emulator"
	mirror     = "mirror.test/emulator"
)

// pullProgress is the start of every pull stream: the engine has answered
// 200 before it fetches anything.
const pullProgress = `{"status":"Pulling from emulator","id":"sha256"}` + "\n" +
	`{"status":"Pulling fs layer","progressDetail":{},"id":"0123456789ab"}` + "\n"

const (
	pulledBody = pullProgress + `{"status":"Status: Downloaded newer image"}` + "\n"
	badGateway = pullProgress +
		`{"errorDetail":{"code":502,"message":"received unexpected HTTP status: 502 Bad Gateway"},` +
		`"error":"received unexpected HTTP status: 502 Bad Gateway"}` + "\n"
	cutOff = pullProgress +
		`{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}` + "\n"
)

// fakeRepo is how the fake engine answers a pull from one repository.
type fakeRepo struct {
	// status, when not 0, refuses the pull with that status before any
	// stream, as the engine does for a reference it cannot resolve.
	status int
	body   string
	// stores says whether a pull that answered 200 leaves the image there.
	stores bool
}

// fakeEngine models the two Docker Engine API routes ensureImage calls:
// image inspect, which finds an image once it is held, and image create (a
// pull), which answers as its repository's fakeRepo says.
type fakeEngine struct {
	repos map[string]fakeRepo
	// hold, when set, delays each pull's answer until it is closed.
	hold chan struct{}

	mu   sync.Mutex
	held map[string]bool
	// pulls lists each pull's repository and X-Registry-Auth, in order.
	pulls []string
}

// repository is ref without its digest.
func repository(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	return name
}

func (e *fakeEngine) start(t *testing.T) imageAPI {
	t.Helper()
	if e.held == nil {
		e.held = map[string]bool{}
	}
	r := chi.NewRouter()
	r.Get("/{version}/images/*", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSuffix(chi.URLParam(r, "*"), "/json")
		e.mu.Lock()
		held := e.held[repository(ref)]
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !held {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"message":"No such image: %s"}`, ref)
			return
		}
		_, _ = w.Write([]byte(`{"Id":"sha256:1"}`))
	})
	r.Post("/{version}/images/create", func(w http.ResponseWriter, r *http.Request) {
		repo := r.URL.Query().Get("fromImage")
		e.mu.Lock()
		e.pulls = append(e.pulls, repo+" "+r.Header.Get("X-Registry-Auth"))
		e.mu.Unlock()
		if e.hold != nil {
			<-e.hold
		}
		answer, ok := e.repos[repo]
		if !ok {
			answer = fakeRepo{status: http.StatusNotFound}
		}
		w.Header().Set("Content-Type", "application/json")
		if answer.status != 0 {
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(`{"message":"pull refused"}`))
			return
		}
		_, _ = w.Write([]byte(answer.body))
		if answer.stores {
			e.mu.Lock()
			e.held[repo] = true
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

func (e *fakeEngine) pulled() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.pulls)
}

// authFor stands for registryAuth: each source gets its own registry's
// credentials.
func authFor(ref string) string {
	host, _, _ := strings.Cut(ref, "/")
	return "cred-" + host
}

// TestUnitEnsureImage covers each answer a pull can give, from one source
// and from two. The stream cases are #1448: testcontainers-go drains the
// stream without decoding it, so it took them for a finished pull and the
// container create then failed with "No such image".
func TestUnitEnsureImage(t *testing.T) {
	one := []string{primary + "@" + testDigest}
	two := []string{primary + "@" + testDigest, mirror + "@" + testDigest}
	cases := []struct {
		name    string
		sources []string
		engine  *fakeEngine
		// want is the source returned; pulls lists each pull's repository
		// and credentials; wantErr lists parts the error must contain.
		want    string
		pulls   []string
		wantErr []string
	}{
		{
			name:    "present already",
			sources: one,
			engine:  &fakeEngine{held: map[string]bool{primary: true}},
			want:    one[0],
		},
		{
			name:    "pulled",
			sources: one,
			engine: &fakeEngine{
				repos: map[string]fakeRepo{primary: {body: pulledBody, stores: true}},
			},
			want:  one[0],
			pulls: []string{primary + " cred-primary.test"},
		},
		{
			name:    "registry error inside the stream",
			sources: one,
			engine:  &fakeEngine{repos: map[string]fakeRepo{primary: {body: badGateway}}},
			pulls:   []string{primary + " cred-primary.test"},
			wantErr: []string{
				"pull " + one[0] + ": received unexpected HTTP status: 502 Bad Gateway",
			},
		},
		{
			name:    "error inside the stream without a code",
			sources: one,
			engine:  &fakeEngine{repos: map[string]fakeRepo{primary: {body: cutOff}}},
			pulls:   []string{primary + " cred-primary.test"},
			wantErr: []string{"pull " + one[0] + ": unexpected EOF"},
		},
		{
			name:    "stream ends cleanly without the image",
			sources: one,
			engine:  &fakeEngine{repos: map[string]fakeRepo{primary: {body: pullProgress}}},
			pulls:   []string{primary + " cred-primary.test"},
			wantErr: []string{
				"pull " + one[0] + " ended without an error, but the engine does not have it",
			},
		},
		{
			name:    "pull refused",
			sources: one,
			engine: &fakeEngine{repos: map[string]fakeRepo{
				primary: {status: http.StatusInternalServerError},
			}},
			pulls:   []string{primary + " cred-primary.test"},
			wantErr: []string{"pull " + one[0] + ": "},
		},
		{
			name:    "first source pulled, second not asked",
			sources: two,
			engine: &fakeEngine{repos: map[string]fakeRepo{
				primary: {body: pulledBody, stores: true},
				mirror:  {body: pulledBody, stores: true},
			}},
			want:  two[0],
			pulls: []string{primary + " cred-primary.test"},
		},
		{
			name:    "first source fails inside its stream, second pulled",
			sources: two,
			engine: &fakeEngine{repos: map[string]fakeRepo{
				primary: {body: badGateway},
				mirror:  {body: pulledBody, stores: true},
			}},
			want:  two[1],
			pulls: []string{primary + " cred-primary.test", mirror + " cred-mirror.test"},
		},
		{
			name:    "second source held already",
			sources: two,
			engine:  &fakeEngine{held: map[string]bool{mirror: true}},
			want:    two[1],
		},
		{
			name:    "every source fails",
			sources: two,
			engine: &fakeEngine{repos: map[string]fakeRepo{
				primary: {body: badGateway},
				mirror:  {body: cutOff},
			}},
			pulls: []string{primary + " cred-primary.test", mirror + " cred-mirror.test"},
			wantErr: []string{
				"pull " + two[0] + ": received unexpected HTTP status: 502 Bad Gateway",
				"pull " + two[1] + ": unexpected EOF",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := tc.engine.start(t)
			got, err := ensureImage(context.Background(), api, authFor, tc.sources)
			if len(tc.wantErr) == 0 && err != nil {
				t.Fatalf("error %v, want none", err)
			}
			for _, part := range tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), part) {
					t.Fatalf("error %v, want one containing %q", err, part)
				}
			}
			if got != tc.want {
				t.Fatalf("source %q, want %q", got, tc.want)
			}
			if pulls := tc.engine.pulled(); !slices.Equal(pulls, tc.pulls) {
				t.Fatalf("pulls %q, want %q", pulls, tc.pulls)
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
		repos: map[string]fakeRepo{primary: {body: pulledBody, stores: true}},
		hold:  make(chan struct{}),
	}
	api := engine.start(t)
	sources := []string{primary + "@" + testDigest}
	const callers = 8
	var started, done sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		started.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			started.Done()
			_, err := ensureImage(context.Background(), api, authFor, sources)
			errs <- err
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
	if pulls := engine.pulled(); len(pulls) != 1 {
		t.Fatalf("pulls %q, want 1", pulls)
	}
}

// TestUnitEmulatorSourcesShareADigest: every source of an emulator names
// the pinned digest, so a second source gives the same bytes, and the first
// source is the exported image.
func TestUnitEmulatorSourcesShareADigest(t *testing.T) {
	for image, sources := range map[string][]string{
		MinIOImage:   minioSources,
		FakeGCSImage: fakeGCSSources,
		AzuriteImage: azuriteSources,
	} {
		if sources[0] != image {
			t.Fatalf("first source %q, want %q", sources[0], image)
		}
		_, digest, _ := strings.Cut(image, "@")
		for _, ref := range sources {
			if _, d, _ := strings.Cut(ref, "@"); d != digest {
				t.Fatalf("source %q does not carry %s's digest %s", ref, image, digest)
			}
		}
	}
}

package peers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

// fileAssertionFailure lets a test observe a helper's Fatalf without marking
// the enclosing scenario failed. Every other testing.TB operation still uses t.
type fileAssertionFailure string

type fileAssertionTB struct{ testing.TB }

func (t fileAssertionTB) Fatalf(format string, args ...any) {
	panic(fileAssertionFailure(fmt.Sprintf(format, args...)))
}

func fileFailure(t *testing.T, check func(testing.TB)) (failure string) {
	t.Helper()
	defer func() {
		if value := recover(); value != nil {
			got, ok := value.(fileAssertionFailure)
			if !ok {
				panic(value)
			}
			failure = string(got)
		}
	}()
	check(fileAssertionTB{t})
	return ""
}

// TestAccPeerFileAssertionsSQLite exercises the shared helpers through a real
// instance client. Injected HTTP failures must fail negative assertions even
// though the generic and Maven files remain present on the actual server.
func TestAccPeerFileAssertionsSQLite(t *testing.T) {
	t.Parallel()
	c := peers.Start(t, peers.Options{Instances: 1, Driver: peers.SQLite})
	in := c.Instances[0]
	paths := []string{"assert-generic/present.txt", "assert-maven/com/example/lib/1.0/lib-1.0.jar"}
	for _, repo := range []struct{ key, kind string }{{"assert-generic", "generic"}, {"assert-maven", "maven"}} {
		do(t, in, http.MethodPut, "/artifactory/api/repositories/"+repo.key,
			`{"rclass":"local","packageType":"`+repo.kind+`"}`, http.StatusOK)
	}
	for _, path := range paths {
		do(t, in, http.MethodPut, "/artifactory/"+path, "present", http.StatusCreated)
		awaitFile(t, []*peers.Instance{in}, path, "present")
		awaitFile(t, []*peers.Instance{in}, path+".missing", "")
	}

	var status, requests atomic.Int64
	response := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		code := int(status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte("present"))
		} else {
			_, _ = w.Write([]byte("controlled HTTP response"))
		}
	}))
	defer response.Close()
	originalURL := in.URL
	defer func() { in.URL = originalURL }()
	in.URL = response.URL
	checks := []struct {
		name string
		run  func(testing.TB, string)
	}{
		{"file", func(tb testing.TB, p string) { file(tb, in, p) }},
		{"awaitFile", func(tb testing.TB, p string) { awaitFile(tb, []*peers.Instance{in}, p, "") }},
		{"absent", func(tb testing.TB, p string) { absent(tb, []*peers.Instance{in}, p) }},
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		status.Store(int64(code))
		for _, check := range checks {
			for _, path := range paths {
				t.Run(strconv.Itoa(code)+"/"+check.name+"/"+strings.Split(path, "/")[0], func(t *testing.T) {
					before := requests.Load()
					failure := fileFailure(t, func(tb testing.TB) { check.run(tb, path) })
					if !strings.Contains(failure, strconv.Itoa(code)+" controlled HTTP response") {
						t.Errorf("helper did not report HTTP %d: %q", code, failure)
					}
					if got := requests.Load() - before; got != 1 {
						t.Errorf("helper made %d requests, want one failure without retries", got)
					}
				})
			}
		}
	}

	status.Store(http.StatusNotFound)
	for _, path := range paths {
		if _, present := file(t, in, path); present {
			t.Fatal("404 was classified as present")
		}
		awaitFile(t, []*peers.Instance{in}, path, "")
		absent(t, []*peers.Instance{in}, path)
	}
	status.Store(http.StatusOK)
	for _, path := range paths {
		if got, present := file(t, in, path); !present || got != "present" {
			t.Fatalf("200 content control: %t %q", present, got)
		}
		awaitFile(t, []*peers.Instance{in}, path, "present")
		if failure := fileFailure(t, func(tb testing.TB) { absent(tb, []*peers.Instance{in}, path) }); !strings.Contains(failure, "received "+path) {
			t.Fatalf("present file passed absent: %q", failure)
		}
	}

	// A closed endpoint produces a genuine transport failure in Instance.Do.
	response.Close()
	for _, check := range checks {
		for _, path := range paths {
			failure := fileFailure(t, func(tb testing.TB) { check.run(tb, path) })
			if !strings.Contains(failure, "GET /artifactory/"+path+" on "+in.Name+":") {
				t.Fatalf("%s hid transport failure: %q", check.name, failure)
			}
		}
	}
	in.URL = originalURL
	for _, path := range paths {
		awaitFile(t, []*peers.Instance{in}, path, "present")
	}
}

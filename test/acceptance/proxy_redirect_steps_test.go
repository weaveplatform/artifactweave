package acceptance

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

type proxyRedirectFixture struct{ origin, target, proxy *fakeupstream.Server }

func (w *world) registerProxyRedirect(sc *godog.ScenarioContext) {
	sc.Step(`^the controlled proxy redirect upstream is ready$`, w.proxyRedirectUpstream)
	sc.Step(`^I apply the proxy redirect configuration:$`, func(body *godog.DocString) error {
		copyBody := *body
		copyBody.Content = w.expand(body.Content)
		return w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &copyBody)
	})
	sc.Step(`^the proxy redirect credential observations are correct$`, w.proxyRedirectObservations)
}

func (w *world) proxyRedirectUpstream() error {
	if os.Getenv("ARTIFACTWEAVE_JF_BIN") == "" {
		return fmt.Errorf("proxy redirect qualification requires ARTIFACTWEAVE_JF_BIN with the pinned JFrog CLI")
	}
	if _, err := w.filesDir(); err != nil {
		return err
	}
	target := fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}})
	origin := fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "repo-user", Password: "repo-secret"}})
	for _, path := range []string{"allowed.bin", "refused.bin", "removed.bin", "malformed.bin", "rotated.bin"} {
		target.Set("/"+path, fakeupstream.Resource{Body: []byte("proxy redirected bytes")})
		origin.Set("/"+path, fakeupstream.Resource{Redirect: target.URL + "/" + path})
	}
	origin.Set("/same.bin", fakeupstream.Resource{Redirect: "/same-content.bin"})
	origin.Set("/same-content.bin", fakeupstream.Resource{Body: []byte("same origin bytes")})
	u, err := url.Parse(target.URL)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp6", net.JoinHostPort("::1", u.Port()))
	if err != nil {
		return err
	}
	proxy := fakeupstream.NewProxyOn(w.t, listener, fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"}, origin, target)
	w.proxyRedirect = &proxyRedirectFixture{origin: origin, target: target, proxy: proxy}
	w.closers = append(w.closers, origin.Close, target.Close, proxy.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["redirect-origin"] = origin.URL
	w.vars["redirect-port"] = u.Port()
	w.env = append(w.env, "ARTIFACTWEAVE_REMOTE_ALLOWED_PRIVATE_ORIGINS="+target.URL)
	return nil
}

func (w *world) proxyRedirectObservations() error {
	f := w.proxyRedirect
	if f == nil {
		return fmt.Errorf("proxy redirect fixture absent")
	}
	counts := map[string]int{}
	for _, r := range f.target.Requests() {
		counts[r.Path]++
		if r.Method != http.MethodGet {
			return fmt.Errorf("unexpected redirect target method %s", r.Method)
		}
		for _, header := range []string{"Proxy-Authorization", "X-Origin-Secret", "Cookie"} {
			if r.Header.Get(header) != "" {
				return fmt.Errorf("redirect target received %s", header)
			}
		}
		if r.Path == "/allowed.bin" {
			if counts[r.Path] == 1 && r.Header.Get("Authorization") != "" {
				return fmt.Errorf("proxy target credentials sent before challenge")
			}
			if counts[r.Path] == 2 && (!r.Authorized || !r.HeaderMatches("Authorization", "Basic cHJveHktdXNlcjpwcm94eS1zZWNyZXQ=")) {
				return fmt.Errorf("proxy credential challenge was not answered")
			}
		} else if r.Header.Get("Authorization") != "" {
			return fmt.Errorf("refused redirect received credentials")
		}
	}
	// The native client makes a second downstream request after a 401 even
	// with transport retries disabled. Both unlisted attempts stay anonymous.
	for path, want := range map[string]int{"/allowed.bin": 2, "/refused.bin": 2, "/removed.bin": 2, "/malformed.bin": 0, "/rotated.bin": 0} {
		if counts[path] != want {
			return fmt.Errorf("target %s count %d, want %d", path, counts[path], want)
		}
	}
	for _, r := range f.origin.Requests() {
		if !r.Authorized || !r.HeaderMatches("X-Origin-Secret", "origin-only") || r.Header.Get("Proxy-Authorization") != "" {
			return fmt.Errorf("origin credential boundary changed")
		}
	}
	for _, r := range f.proxy.Requests() {
		if r.HeaderMatches("Authorization", "Basic cmVwby11c2VyOnJlcG8tc2VjcmV0") && r.Method == http.MethodConnect {
			return fmt.Errorf("repository credential reached CONNECT")
		}
		if r.Host == "" {
			return fmt.Errorf("proxy request lacked destination")
		}
	}
	w.t.Log(
		"native proxy redirect effects: permitted=one anonymous+one proxy Basic; refused/removed=two anonymous native 401 attempts; malformed/rotated=no target; repository and proxy headers isolated",
	)
	return nil
}

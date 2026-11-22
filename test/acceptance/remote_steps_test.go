package acceptance

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

func (w *world) registerRemote(sc *godog.ScenarioContext) {
	sc.Step(`^the NTLM proxy native client is available$`, w.ntlmNativeClient)
	sc.Step(`^I PATCH "([^"]*)" as admin with expanded YAML:$`, func(path string, body *godog.DocString) error {
		return w.patchYAMLAsAdmin(path, &godog.DocString{Content: w.expand(body.Content)})
	})
	sc.Step(`^an NTLM proxy protects the generic upstream$`, w.remoteNTLMProxy)
	sc.Step(`^the NTLM proxy verified one domain and workstation exchange$`, w.remoteNTLMVerified)
	sc.Step(`^the generic upstream received a GET for "([^"]*)" with origin headers$`, w.remoteOriginHeaders)
	w.registerSmartRemote(sc)
	w.registerRemoteNPM(sc)
	w.registerRemoteGo(sc)
	w.registerRemoteHelm(sc)
	w.registerNIM(sc)
	sc.Step(`^a remote directory index at "([^"]*)" contains:$`, w.remoteDirectoryIndex)
	w.registerRemoteSearch(sc)
	sc.Step(`^a generic upstream serves "([^"]*)" with content "([^"]*)"$`, w.remoteUpstream)
	sc.Step(`^the generic upstream stops$`, func() { w.remote.Close() })
	sc.Step(
		`^the generic upstream received (\d+) (GET|HEAD) requests for "([^"]*)"$`,
		w.remoteRequests,
	)
	sc.Step(`^I run curl "([^"]*)"$`, w.runCurl)
}

func (w *world) ntlmNativeClient() error {
	if os.Getenv("ARTIFACTWEAVE_JF_BIN") == "" {
		return errors.New("NTLM proxy acceptance requires ARTIFACTWEAVE_JF_BIN from the pinned JFrog CLI action")
	}
	_, err := w.filesDir()
	return err
}

func (w *world) remoteNTLMProxy() error {
	w.ntlmProxy = fakeupstream.NewNTLMProxy(w.t, fakeupstream.NTLMOptions{
		Username: "proxy-user", Password: "proxy-fixture-password", Domain: "CORP", Workstation: "ARTIFACTWEAVE", Forward: true, ForwardOnly: true,
	}, w.remote)
	w.closers = append(w.closers, w.ntlmProxy.Close)
	u, err := url.Parse(w.ntlmProxy.URL)
	if err != nil {
		return err
	}
	w.vars["proxyhost"], w.vars["proxyport"] = u.Hostname(), u.Port()
	return nil
}

func (w *world) remoteNTLMVerified() error {
	for path, want := range map[string]string{"first/first.bin": "first NTLM native bytes", "second/second.bin": "second NTLM native bytes"} {
		got, err := os.ReadFile(filepath.Join(w.dir, path))
		if err != nil {
			return err
		}
		if string(got) != want {
			return fmt.Errorf("native download %s differs from its complete upstream body", path)
		}
	}
	records := w.ntlmProxy.Negotiations()
	if len(records) < 3 || records[0].Message != 0 || records[1].Message != 1 ||
		records[2].Message != 3 || !records[2].Authorized || records[2].Domain != "CORP" ||
		records[2].Workstation != "ARTIFACTWEAVE" || records[1].Domain != "CORP" || records[1].Workstation != "ARTIFACTWEAVE" {
		return fmt.Errorf("NTLM proof/domain/workstation exchange differs: %+v", records)
	}
	negotiated, authenticated := 0, 0
	for _, record := range records {
		if record.Message == 1 {
			negotiated++
		}
		if record.Message == 3 {
			authenticated++
		}
	}
	if negotiated != 1 || authenticated != 1 {
		return errors.New("native proxy negotiated more than one identity")
	}
	for _, record := range records {
		if record.Connection != records[0].Connection || record.OriginAuthorization {
			return errors.New("NTLM negotiation changed socket or disclosed origin credentials")
		}
	}
	for _, record := range w.remote.Requests() {
		if record.Header.Get("Proxy-Authorization") != "" || record.Header.Get("Authorization") != "" {
			return errors.New("native, origin and proxy credentials crossed")
		}
	}
	return nil
}

func (w *world) remoteUpstream(path, body string) error {
	if w.remote == nil {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
		w.closers = append(w.closers, w.remote.Close)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	}
	w.remote.Set(path, fakeupstream.Resource{Body: []byte(body)})
	return nil
}

func (w *world) remoteRequests(want int, method, path string) error {
	if got := w.remote.Count(method, path); got != want {
		return fmt.Errorf("upstream %s %s count %d, want %d", method, path, got, want)
	}
	return nil
}

func (w *world) runCurl(args string) error {
	if _, err := w.filesDir(); err != nil {
		return err
	}
	cmd := exec.Command("curl", strings.Fields(w.expand(args))...)
	cmd.Dir = w.dir
	cmd.Env = os.Environ()
	w.markCommand()
	out, err := cmd.CombinedOutput()
	w.body, w.lastRC = out, 0
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			w.lastRC = exit.ExitCode()
			return nil
		}
		return err
	}
	return nil
}

func (w *world) remoteOriginHeaders(path string) error {
	for _, r := range w.remote.Requests() {
		if r.Method == "GET" && r.Path == path && r.Header.Get(remote.OriginHeader) != "" && r.Header.Get(remote.OriginatedHeader) != "" {
			return nil
		}
	}
	return fmt.Errorf("no upstream GET for %s carried origin headers", path)
}

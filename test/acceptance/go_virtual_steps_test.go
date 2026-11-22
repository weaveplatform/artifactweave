package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/cucumber/godog"
	gomodule "golang.org/x/mod/module"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerRemoteGo(sc *godog.ScenarioContext) {
	sc.Step(`^the Go toolchain is required$`, func() error {
		if _, err := exec.LookPath("go"); err != nil {
			return fmt.Errorf("required Go client unavailable: %w", err)
		}
		return nil
	})
	sc.Step(`^a descriptor service-string proxy routes the private Go upstream$`, w.privateGoProxy)
	sc.Step(`^the private Go module was retrieved through its descriptor proxy$`, w.privateGoProxyRequests)
	sc.Step(`^a private Go upstream serves module "([^"]*)" at "([^"]*)"$`, w.privateGoUpstream)
}

func (w *world) privateGoUpstream(name, version string) error {
	if err := w.writeGoModule(name, version, "go-upstream"); err != nil {
		return err
	}
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "go-upstream", Password: "upstream-pass"}})
	w.closers = append(w.closers, w.remote.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	escaped, err := gomodule.EscapePath(name)
	if err != nil {
		return fmt.Errorf("escape module: %w", err)
	}
	for _, file := range []string{"list", version + ".info", version + ".mod", version + ".zip"} {
		data, readErr := os.ReadFile(filepath.Join(w.dir, "go-upstream", name, "@v", file))
		if readErr != nil {
			return fmt.Errorf("read upstream module: %w", readErr)
		}
		w.remote.Set("/"+escaped+"/@v/"+file, fakeupstream.Resource{Body: data})
	}
	return nil
}

func (w *world) privateGoProxy() error {
	w.goOutboundProxy = fakeupstream.NewProxy(w.t, fakeupstream.Auth{Username: "go-proxy", Password: "go-proxy-private"}, w.remote)
	w.closers = append(w.closers, w.goOutboundProxy.Close)
	address, err := url.Parse(w.goOutboundProxy.URL)
	if err != nil {
		return fmt.Errorf("parse fixture proxy: %w", err)
	}
	patch := fmt.Sprintf(
		"proxies:\n  go-services:\n    host: %s\n    port: %s\n    username: go-proxy\n    password: go-proxy-private\n    services: 'jfrt,jfxr'\n",
		address.Hostname(),
		address.Port(),
	)
	return w.patchYAMLAsAdmin("/artifactory/api/system/configuration", &godog.DocString{Content: patch})
}

func (w *world) privateGoProxyRequests() error {
	path := "/example.com/!team/!remote/@v/v0.0.0-20261016120000-abcdefabcdef.zip"
	if w.goOutboundProxy.Count(http.MethodGet, path) != 1 {
		return fmt.Errorf("private Go module did not cross its descriptor-created proxy exactly once")
	}
	for _, request := range w.goOutboundProxy.Requests() {
		if !request.Authorized {
			return fmt.Errorf("Go outbound proxy rejected its configured credential")
		}
	}
	for _, request := range w.remote.Requests() {
		if !request.Authorized || request.Header.Get("Proxy-Authorization") != "" {
			return fmt.Errorf("Go origin/proxy credential separation failed")
		}
	}
	return nil
}

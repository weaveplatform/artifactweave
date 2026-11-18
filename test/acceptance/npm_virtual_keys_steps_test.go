package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

func (w *world) registerNPMVirtualKeys(sc *godog.ScenarioContext) {
	sc.Step(`^a private signing npm upstream serves "([^"]*)" at "([^"]*)"$`, w.privateNPMKeysUpstream)
	sc.Step(`^virtual npm keys preserve both publishers and the local version owner$`, w.virtualNPMKeysPublishers)
	sc.Step(`^the private npm signing upstream received only configured credentials$`, w.virtualNPMKeysCredentials)
}

func (w *world) privateNPMKeysUpstream(name, version string) error {
	if w.remote == nil {
		w.remote = fakeupstream.New(w.t, fakeupstream.Options{Auth: fakeupstream.Auth{Username: "npm-origin", Password: "npm-origin-secret"}})
		w.closers = append(w.closers, w.remote.Close)
		if w.vars == nil {
			w.vars = map[string]string{}
		}
		w.vars["upstream"] = w.remote.URL
	}
	return w.npmUpstream(name, version)
}

func (w *world) virtualNPMKeysCredentials() error {
	if w.remote == nil || len(w.remote.Requests()) == 0 {
		return fmt.Errorf("private npm origin was not read")
	}
	for _, request := range w.remote.Requests() {
		if !request.Authorized {
			return fmt.Errorf("private npm origin request lacked configured credentials: %s", request.Path)
		}
	}
	return nil
}

func (w *world) virtualNPMKeysPublishers() error {
	get := func(path string) error {
		if err := w.send(http.MethodGet, path, "", func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) }); err != nil {
			return err
		}
		return w.statusIs(200)
	}
	root := "/artifactory/api/npm/npm-signing-all/"
	if err := get(root + "-/npm/v1/keys"); err != nil {
		return err
	}
	var listed struct {
		Keys []npmfmt.Key `json:"keys"`
	}
	if err := json.Unmarshal(w.body, &listed); err != nil {
		return err
	}
	upstream, err := npmUpstreamKey()
	if err != nil {
		return err
	}
	remote, err := npmfmt.PublicKey(&upstream.PublicKey)
	if err != nil {
		return err
	}
	if len(listed.Keys) != 2 || listed.Keys[0].KeyID == remote.KeyID || listed.Keys[1].KeyID != remote.KeyID {
		return fmt.Errorf("virtual keys did not retain both ordered publishers: %s", w.body)
	}
	for i, name := range []string{"local-signed", "remote-signed"} {
		if err := get(root + name + "/1.0.0"); err != nil {
			return err
		}
		var manifest struct {
			Dist struct {
				Signatures []npmfmt.Signature `json:"signatures"`
			} `json:"dist"`
		}
		if err := json.Unmarshal(w.body, &manifest); err != nil {
			return err
		}
		if len(manifest.Dist.Signatures) != 1 || manifest.Dist.Signatures[0].KeyID != listed.Keys[i].KeyID {
			return fmt.Errorf("%s lost its complete winning member signature: %s", name, w.body)
		}
	}
	return nil
}

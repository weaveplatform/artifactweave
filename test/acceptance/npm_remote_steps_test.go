package acceptance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
)

func (w *world) registerRemoteNPM(sc *godog.ScenarioContext) {
	sc.Step(`^an npm upstream serves "([^"]*)" at "([^"]*)"$`, w.npmUpstream)
	w.registerNPMSignatures(sc)
}

// npmUpstreamKey is the key every fake npm upstream signs its versions with
// and lists at /-/npm/v1/keys, as registry.npmjs.org signs every package.
// It is generated once per run, never committed.
var npmUpstreamKey = sync.OnceValues(func() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
})

// npmUpstream serves name at version from the generic upstream, as an npm
// registry would: its packument, with the version signed by
// npmUpstreamKey over what npm verifies, "<name>@<version>:<integrity>",
// its tarball, and the keys document that lists the key.
func (w *world) npmUpstream(name, version string) error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	path := name + "/-/" + strings.TrimPrefix(name, "@") + "-" + version + ".tgz"
	if err := w.writeNPMPackage(name, version, "/unused", "npm-upstream"); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(w.dir, "npm-upstream", name+".json"))
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	dist := doc["versions"].(map[string]any)[version].(map[string]any)["dist"].(map[string]any)
	// Relative references exercise the same production rewrite as registry URLs.
	dist["tarball"] = "/" + path
	key, err := npmUpstreamKey()
	if err != nil {
		return err
	}
	pub, err := npmfmt.PublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(name + "@" + version + ":" + dist["integrity"].(string)))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return err
	}
	dist["signatures"] = []any{
		map[string]string{"keyid": pub.KeyID, "sig": base64.StdEncoding.EncodeToString(sig)},
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	jsonHeader := map[string][]string{"Content-Type": {"application/json"}}
	w.remote.Set("/"+name, fakeupstream.Resource{Body: raw, Header: jsonHeader})
	keys, err := json.Marshal(map[string][]npmfmt.Key{"keys": {pub}})
	if err != nil {
		return err
	}
	w.remote.Set("/-/npm/v1/keys", fakeupstream.Resource{Body: keys, Header: jsonHeader})
	tgz, err := os.ReadFile(filepath.Join(w.dir, "npm-upstream", name+"-"+version+".tgz"))
	if err != nil {
		return err
	}
	w.remote.Set(
		"/"+path,
		fakeupstream.Resource{
			Body:   tgz,
			Header: map[string][]string{"Content-Type": {"application/octet-stream"}},
		},
	)
	return nil
}

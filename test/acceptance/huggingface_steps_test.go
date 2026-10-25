package acceptance

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerHuggingFace(sc *godog.ScenarioContext) {
	sc.Step(
		`^a controlled Hugging Face model and dataset upstream is available$`,
		w.huggingFaceUpstream,
	)
	sc.Step(
		`^the pinned Hugging Face client (publishes and downloads|downloads) model and dataset through "([^"]*)"$`,
		w.huggingFaceClient,
	)
}

func (w *world) huggingFaceUpstream() error {
	w.remote = fakeupstream.New(w.t, fakeupstream.Options{})
	w.closers = append(w.closers, w.remote.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = w.remote.URL
	for _, kind := range []string{"models", "datasets"} {
		label := strings.TrimSuffix(kind, "s")
		config := []byte(`{"kind":"` + label + `"}`)
		weights := []byte(strings.Repeat("X", 1024*1024+1))
		sum := sha256.Sum256(weights)
		oid := hex.EncodeToString(sum[:])
		pointer := fmt.Sprintf(
			"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
			oid,
			len(weights),
		)
		blobID := func(data []byte) string {
			h := sha1.New()
			_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
			_, _ = h.Write(data)
			return hex.EncodeToString(h.Sum(nil))
		}
		info := map[string]any{
			"id":  "acme/demo",
			"sha": "0123456789012345678901234567890123456789",
			"siblings": []map[string]any{
				{"rfilename": "config.json", "size": len(config), "blobId": blobID(config)},
				{
					"rfilename": "weights.bin",
					"size":      len(weights),
					"blobId":    blobID([]byte(pointer)),
					"lfs": map[string]any{
						"sha256":      oid,
						"size":        len(weights),
						"pointerSize": len(pointer),
					},
				},
			},
		}
		body, e := json.Marshal(info)
		if e != nil {
			return e
		}
		prefix := "/"
		if kind == "datasets" {
			prefix = "/datasets/"
		}
		for _, revision := range []string{"main", "0123456789012345678901234567890123456789"} {
			w.remote.Set(
				"/api/"+kind+"/acme/demo/revision/"+revision,
				fakeupstream.Resource{
					Body:   body,
					Header: http.Header{"Content-Type": []string{"application/json"}},
				},
			)
			w.remote.Set(
				prefix+"acme/demo/resolve/"+revision+"/config.json",
				fakeupstream.Resource{Body: config},
			)
			w.remote.Set(
				prefix+"acme/demo/resolve/"+revision+"/weights.bin",
				fakeupstream.Resource{Body: weights},
			)
		}
	}
	return nil
}

func (w *world) huggingFaceClient(action, repoKey string) error {
	python := os.Getenv("ARTIFACTWEAVE_HF_PYTHON")
	if python == "" {
		return fmt.Errorf(
			"ARTIFACTWEAVE_HF_PYTHON must point to pinned huggingface_hub 1.18.0 and hf_xet 1.4.3",
		)
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	token := w.vars["huggingface-token"]
	if token == "" {
		e := w.send(
			"POST",
			"/access/api/v1/tokens",
			`{"expires_in":3600}`,
			func(r *http.Request) { r.SetBasicAuth("admin", testAdminPassword) },
		)
		if e != nil {
			return e
		}
		if e = w.statusIs(200); e != nil {
			return e
		}
		var result struct {
			AccessToken string `json:"access_token"`
		}
		if e = json.Unmarshal(w.body, &result); e != nil {
			return e
		}
		if result.AccessToken == "" {
			return fmt.Errorf("token response is empty")
		}
		token = result.AccessToken
		w.vars["huggingface-token"] = token
	}
	mode := "download"
	if action == "publishes and downloads" {
		mode = "publish"
	}
	root, e := os.MkdirTemp("", "artifactweave-hf-client-")
	if e != nil {
		return e
	}
	w.closers = append(w.closers, func() { _ = os.RemoveAll(root) })
	script := filepath.Join("testdata", "huggingface_client.py")
	cmd := exec.Command(python, script, mode, root)
	cmd.Env = append(
		os.Environ(),
		"HF_ENDPOINT="+w.url+"/artifactory/api/huggingfaceml/"+repoKey,
		"HF_TOKEN="+token,
		"HF_HOME="+filepath.Join(root, "hub-cache"),
		"HF_HUB_DISABLE_XET=1",
		"HF_HUB_DISABLE_TELEMETRY=1",
	)
	out, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("Hugging Face client %s %s: %w: %s\nserver: %s", mode, repoKey, e, out,
			w.stderr.String())
	}
	return nil
}

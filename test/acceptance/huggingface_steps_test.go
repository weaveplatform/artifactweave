package acceptance

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	client := os.Getenv("ARTIFACTWEAVE_HF_BIN")
	if client == "" {
		return fmt.Errorf(
			"ARTIFACTWEAVE_HF_BIN must point to the pinned Hugging Face Hub 2.3.0 CLI",
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
	root, e := os.MkdirTemp("", "artifactweave-hf-client-")
	if e != nil {
		return e
	}
	w.closers = append(w.closers, func() { _ = os.RemoveAll(root) })
	endpoint := w.url + "/artifactory/api/huggingfaceml/" + repoKey
	env := append(
		os.Environ(),
		"HF_ENDPOINT="+endpoint,
		"HF_TOKEN="+token,
		"HF_HOME="+filepath.Join(root, "hub-cache"),
		"HF_HUB_DISABLE_XET=1",
		"HF_HUB_DISABLE_TELEMETRY=1",
	)
	for _, kind := range []string{"model", "dataset"} {
		fixture := filepath.Join("testdata", "huggingface", kind, "config.json")
		config, err := os.ReadFile(fixture)
		if err != nil {
			return err
		}
		if action == "publishes and downloads" {
			source := filepath.Join(root, kind+"-source")
			if err = os.Mkdir(source, 0o700); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(source, "config.json"), config, 0o600); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(source, "weights.bin"), bytes.Repeat([]byte("X"), 1<<20+1), 0o600); err != nil {
				return err
			}
			if err = w.publishHuggingFaceFiles(client, env, source, kind, ""); err != nil {
				return err
			}
		}
		if err = verifyHuggingFaceTree(endpoint, token, kind); err != nil {
			return err
		}
		destination := filepath.Join(root, kind+"-download")
		if err = w.runHuggingFaceCLI(client, env, "download", "acme/demo", "--repo-type", kind, "--local-dir", destination, "--max-workers", "1"); err != nil {
			return err
		}
		if err = verifyHuggingFaceDownload(destination, config); err != nil {
			return fmt.Errorf("%s snapshot: %w", kind, err)
		}
		if err = verifyHuggingFaceRange(endpoint, token, kind); err != nil {
			return err
		}
	}
	if action == "publishes and downloads" {
		source := filepath.Join(root, "model-source")
		if e = w.publishHuggingFaceFiles(client, env, source, "model", "v1"); e != nil {
			return e
		}
		destination := filepath.Join(root, "named-download")
		if e = w.runHuggingFaceCLI(client, env, "download", "acme/demo", "--repo-type", "model", "--revision", "v1", "--local-dir", destination, "--max-workers", "1"); e != nil {
			return e
		}
		config, err := os.ReadFile(filepath.Join("testdata", "huggingface", "model", "config.json"))
		if err != nil {
			return err
		}
		if e = verifyHuggingFaceDownload(destination, config); e != nil {
			return fmt.Errorf("named model snapshot: %w", e)
		}
	}
	return nil
}

func (w *world) publishHuggingFaceFiles(client string, env []string, source, kind, revision string) error {
	args := []string{"upload", "acme/demo", source, ".", "--repo-type", kind}
	if revision != "" {
		args = append(args, "--revision", revision)
	}
	return w.runHuggingFaceCLI(client, env, args...)
}

func (w *world) runHuggingFaceCLI(client string, env []string, args ...string) error {
	cmd := exec.Command(client, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hf %s: %w: %s\nserver: %s", strings.Join(args, " "), err, out, w.stderr.String())
	}
	return nil
}

func verifyHuggingFaceDownload(destination string, config []byte) error {
	for name, expected := range map[string][]byte{
		"config.json": config,
		"weights.bin": bytes.Repeat([]byte("X"), 1<<20+1),
	} {
		got, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, expected) {
			return fmt.Errorf("%s differs from the published fixture", name)
		}
	}
	return nil
}

func verifyHuggingFaceTree(endpoint, token, kind string) error {
	path := "models"
	if kind == "dataset" {
		path = "datasets"
	}
	req, err := http.NewRequest(http.MethodGet, endpoint+"/api/"+path+"/acme/demo/tree/main?recursive=true", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s tree: HTTP %d: %s", kind, response.StatusCode, body)
	}
	var tree []struct {
		Path string `json:"path"`
	}
	if err = json.Unmarshal(body, &tree); err != nil {
		return err
	}
	if len(tree) != 2 {
		return fmt.Errorf("%s tree contains %d entries, want 2: %s", kind, len(tree), body)
	}
	paths := map[string]bool{"config.json": false, "weights.bin": false}
	for _, entry := range tree {
		if _, ok := paths[entry.Path]; !ok || paths[entry.Path] {
			return fmt.Errorf("%s tree has an unexpected or duplicate path %q", kind, entry.Path)
		}
		paths[entry.Path] = true
	}
	if !paths["config.json"] || !paths["weights.bin"] {
		return fmt.Errorf("%s tree is missing a fixture path: %s", kind, body)
	}
	return nil
}

func verifyHuggingFaceRange(endpoint, token, kind string) error {
	prefix := "/"
	if kind == "dataset" {
		prefix = "/datasets/"
	}
	req, err := http.NewRequest(http.MethodGet, endpoint+prefix+"acme/demo/resolve/main/weights.bin", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", "bytes=100-199")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusPartialContent || !bytes.Equal(body, bytes.Repeat([]byte("X"), 100)) {
		return fmt.Errorf("%s range: HTTP %d, %d bytes with unexpected content", kind, response.StatusCode, len(body))
	}
	return nil
}

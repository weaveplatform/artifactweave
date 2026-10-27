package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
)

// These smart-HTTP responses contain packs produced by the native Git CLI,
// independently of the server's Go decoder and archive implementation.
func (w *world) cocoaPodsGitUpstream() error {
	if err := w.cocoaPodsArchive("HelloPod", "1.0.0"); err != nil {
		return err
	}
	if err := w.cocoaPodsHTTPUpstream("HelloPod-1.0.0.tar.gz"); err != nil {
		return err
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "../fixtures/cocoapods-git")
	data, err := os.ReadFile(filepath.Join(dir, "refs.json"))
	if err != nil {
		return err
	}
	var refs map[string]string
	if err = json.Unmarshal(data, &refs); err != nil {
		return err
	}
	packet := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	for _, fixture := range []struct{ name, path, hash string }{{"root", "main", refs["commit"]}, {"submodule", "dep", refs["submodule"]}} {
		advertised := packet(
			"# service=git-upload-pack\n",
		) + "0000" + packet(
			fixture.hash+" HEAD\x00ofs-delta\n",
		) + packet(
			fixture.hash+" refs/heads/main\n",
		)
		if fixture.name == "root" {
			advertised += packet(refs["tag"] + " refs/tags/1.0.0\n")
		}
		advertised += "0000"
		pack, err := os.ReadFile(filepath.Join(dir, fixture.name+".pack"))
		if err != nil {
			return err
		}
		w.remote.Set(
			"/"+fixture.path+".git/info/refs",
			fakeupstream.Resource{
				Body: []byte(advertised),
				Header: http.Header{
					"Content-Type": {"application/x-git-upload-pack-advertisement"},
				},
			},
		)
		w.remote.Set(
			"/"+fixture.path+".git/git-upload-pack",
			fakeupstream.Resource{
				Method: http.MethodPost,
				Status: 200,
				Body:   append([]byte("0008NAK\n"), pack...),
				Header: http.Header{"Content-Type": {"application/x-git-upload-pack-result"}},
			},
		)
	}
	spec, err := json.Marshal(map[string]any{
		"name":      "HelloPod",
		"version":   "1.0.0",
		"summary":   "Controlled Git upstream pod",
		"homepage":  "https://example.org/HelloPod",
		"license":   map[string]string{"type": "MIT"},
		"authors":   map[string]string{"Example": "example@example.org"},
		"platforms": map[string]string{"ios": "12.0"},
		"source": map[string]any{
			"git":        w.remote.URL + "/main.git",
			"tag":        "1.0.0",
			"submodules": true,
		},
		"source_files": []string{"Sources/*.m", "Vendor/*.m"},
	})
	if err != nil {
		return err
	}
	w.remote.Set("/"+podfmt.SpecPath("HelloPod", "1.0.0"), fakeupstream.Resource{Body: spec})
	return nil
}

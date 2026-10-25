package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/cucumber/godog"
)

// The gallery response is an observed Microsoft response. The fixture changes
// only its source host so the production remote fetch stays hermetic; the
// three downloaded official asset byte streams are never rewritten.
type aiEditorFixture struct {
	server  *httptest.Server
	catalog []byte
	assets  map[string][]byte
	queries atomic.Int32
}

func (w *world) registerAIEditorExtensions(sc *godog.ScenarioContext) {
	sc.Step(`^the official AI editor Marketplace fixture serves Hex Editor$`, w.aiEditorFixture)
	sc.Step(`^I install Hex Editor with VS Code profile "([^"]*)"$`, w.installAIEditorExtension)
	sc.Step(`^the AI editor upstream received (\d+) gallery queries$`, w.aiEditorQueries)
	sc.Step(
		`^the AI editor gallery assets use the client-reachable authority$`,
		w.aiEditorAssetAuthority,
	)
	sc.Step(`^the AI editor upstream stops$`, w.stopAIEditorUpstream)
}

func (w *world) aiEditorFixture() error {
	dir := os.Getenv("ARTIFACTWEAVE_AI_EDITOR_ASSETS")
	if dir == "" {
		return fmt.Errorf("ARTIFACTWEAVE_AI_EDITOR_ASSETS is required for the pinned client")
	}
	files := map[string]struct{ name, sha string }{
		"Microsoft.VisualStudio.Code.Manifest": {
			"manifest.json",
			"a7e639542019cee516ae5a89df82340230924a9c9b93c1e4106726f66651143d",
		},
		"Microsoft.VisualStudio.Services.VSIXPackage": {
			"package.vsix",
			"441e583a9df4b5f304cc6c97a4ec0f207cd7a9995119cfa95e22777e87a0a3b6",
		},
		"Microsoft.VisualStudio.Services.VsixSignature": {
			"signature.zip",
			"0505d7a8c2be5b4072b439ecb9032642ecf640d766b34c36308b7cc20fb0a9c0",
		},
	}
	f := &aiEditorFixture{assets: make(map[string][]byte)}
	for kind, file := range files {
		body, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != file.sha {
			return fmt.Errorf("official %s checksum changed", kind)
		}
		f.assets[kind] = body
	}
	raw, err := os.ReadFile(
		"../../internal/server/aieditorextensions/testdata/marketplace-hexeditor.json",
	)
	if err != nil {
		return err
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	w.closers = append(w.closers, f.server.Close)
	var catalog struct {
		Results []struct {
			Extensions []struct {
				Versions []struct {
					AssetURI         string `json:"assetUri"`
					FallbackAssetURI string `json:"fallbackAssetUri"`
					Files            []struct {
						AssetType string `json:"assetType"`
						Source    string `json:"source"`
					} `json:"files"`
				} `json:"versions"`
			} `json:"extensions"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	for ri := range catalog.Results {
		for ei := range catalog.Results[ri].Extensions {
			for vi := range catalog.Results[ri].Extensions[ei].Versions {
				v := &catalog.Results[ri].Extensions[ei].Versions[vi]
				base := f.server.URL + "/_apis/public/gallery/extensions/ms-vscode/hexeditor/1.11.1/1730650070908"
				v.AssetURI = base
				v.FallbackAssetURI = f.server.URL + "/_apis/public/gallery/publisher/ms-vscode/extension/hexeditor/1.11.1/assetbyname"
				for fi := range v.Files {
					v.Files[fi].Source = base + "/" + v.Files[fi].AssetType
				}
			}
		}
	}
	// Marshal the original map as well, preserving all native fields used by
	// the client. Only the validated URI fields above are replaced below.
	var preserved map[string]any
	if err := json.Unmarshal(raw, &preserved); err != nil {
		return err
	}
	version := preserved["results"].([]any)[0].(map[string]any)["extensions"].([]any)[0].(map[string]any)["versions"].([]any)[0].(map[string]any)
	v := catalog.Results[0].Extensions[0].Versions[0]
	version["assetUri"], version["fallbackAssetUri"] = v.AssetURI, v.FallbackAssetURI
	for i, file := range v.Files {
		version["files"].([]any)[i].(map[string]any)["source"] = file.Source
	}
	f.catalog, err = json.Marshal(preserved)
	if err != nil {
		return err
	}
	w.aiEditor = f
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = f.server.URL
	return nil
}

func (f *aiEditorFixture) serve(w http.ResponseWriter, r *http.Request) {
	const prefix = "/_apis/public/gallery/"
	if r.URL.Path == prefix+"extensionquery" && r.Method == http.MethodPost {
		f.queries.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(f.catalog)
		return
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		strings.HasPrefix(
			r.URL.Path,
			prefix+"extensions/ms-vscode/hexeditor/1.11.1/1730650070908/",
		) {
		name := strings.TrimPrefix(
			r.URL.Path,
			prefix+"extensions/ms-vscode/hexeditor/1.11.1/1730650070908/",
		)
		body, ok := f.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch name {
		case "Microsoft.VisualStudio.Code.Manifest":
			w.Header().Set("Content-Type", "application/json")
		default:
			w.Header().Set("Content-Type", "application/zip")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
		return
	}
	http.NotFound(w, r)
}

func (w *world) installAIEditorExtension(profile string) error {
	if profile == "" || strings.ContainsAny(profile, "/\\.") {
		return fmt.Errorf("invalid VS Code profile")
	}
	gallery := w.url + "/artifactory/api/aieditorextensions/editor-remote/_apis/public/gallery"
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_VSCODE_DESKTOP") == "1" {
		gallery = strings.Replace(gallery, "127.0.0.1", "host.docker.internal", 1)
		script, err := filepath.Abs("clients/vscode-install.sh")
		if err != nil {
			return err
		}
		archive := os.Getenv("ARTIFACTWEAVE_VSCODE_DESKTOP_ARCHIVE")
		if archive == "" {
			return fmt.Errorf("ARTIFACTWEAVE_VSCODE_DESKTOP_ARCHIVE is required")
		}
		return w.runContainer(w.vscodeImage, []string{
			"--user", "0", "-e", "HOME=/tmp", "-e", "ARTIFACTWEAVE_GALLERY_URL=" + gallery,
			"-v", archive + ":/opt/vscode", "-v", script + ":/usr/local/bin/vscode-install:ro",
		}, []string{"sh", "-c", "apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends libgbm1 libasound2t64 python3 >/dev/null && sh /usr/local/bin/vscode-install " + profile})
	}
	return w.runContainer(
		w.vscodeImage,
		[]string{"-e", "ARTIFACTWEAVE_GALLERY_URL=" + gallery},
		[]string{"/usr/local/bin/vscode-install", profile},
	)
}

func (w *world) aiEditorQueries(want int) error {
	if w.aiEditor == nil {
		return fmt.Errorf("AI editor upstream is not running")
	}
	if got := w.aiEditor.queries.Load(); got != int32(want) {
		return fmt.Errorf("gallery upstream received %d queries, want %d", got, want)
	}
	return nil
}

func (w *world) aiEditorAssetAuthority() error {
	const query = `{"filters":[{"criteria":[{"filterType":7,"value":"ms-vscode.hexeditor"},{"filterType":8,"value":"Microsoft.VisualStudio.Code"},{"filterType":12,"value":"4096"}],"pageNumber":1,"pageSize":1,"sortBy":0,"sortOrder":0}],"assetTypes":[],"flags":950}`
	url := w.url + "/artifactory/api/aieditorextensions/editor-remote/_apis/public/gallery/extensionquery"
	resp, err := http.Post(url, "application/json", strings.NewReader(query))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("gallery status %d: %s", resp.StatusCode, b)
	}
	var catalog struct {
		Results []struct {
			Extensions []struct {
				Versions []struct {
					Files []struct {
						Source string `json:"source"`
					} `json:"files"`
				} `json:"versions"`
			} `json:"extensions"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&catalog); err != nil {
		return err
	}
	if len(catalog.Results) != 1 || len(catalog.Results[0].Extensions) != 1 ||
		len(catalog.Results[0].Extensions[0].Versions) == 0 {
		return fmt.Errorf("gallery omitted installable extension")
	}
	want := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_VSCODE_DESKTOP") == "1" {
		want = strings.Replace(want, "127.0.0.1", "host.docker.internal", 1)
	}
	want += "/artifactory/api/aieditorextensions/editor-remote/assets/ms-vscode/hexeditor/1.11.1/"
	files := catalog.Results[0].Extensions[0].Versions[0].Files
	if len(files) != 3 {
		return fmt.Errorf("gallery projected %d install assets, want 3", len(files))
	}
	for _, file := range files {
		if !strings.HasPrefix(file.Source, want) {
			return fmt.Errorf(
				"gallery asset URL %q cannot be reached by the client at %q",
				file.Source,
				want,
			)
		}
	}
	return nil
}

func (w *world) stopAIEditorUpstream() {
	if w.aiEditor != nil {
		w.aiEditor.server.Close()
	}
}

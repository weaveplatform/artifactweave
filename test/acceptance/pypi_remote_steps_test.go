package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	pypifmt "github.com/weaveplatform/artifactweave/internal/format/pypi"
)

func (w *world) registerRemotePyPI(sc *godog.ScenarioContext) {
	sc.Step(`^the Python remote registry is running with JSON "([^"]*)"$`, func(json string) error {
		w.env = append(w.env, "ARTIFACTWEAVE_PYPI_CORE_METADATA=true")
		return w.startWith("ARTIFACTWEAVE_PYPI_SIMPLE_JSON=" + json)
	})
	sc.Step(`^a Python upstream serves a wheel and sdist in "([^"]*)"$`, w.pythonUpstream)
	sc.Step(
		`^I install the remote Python packages with "([^"]*)" into "([^"]*)"$`,
		w.installRemotePython,
	)
}

func (w *world) pythonUpstream(form string) error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	if err := w.writeWheel("acme-smoke", "1.0.0", "dist"); err != nil {
		return err
	}
	if err := w.writeSdist("acme-source", "1.0.0", "dist"); err != nil {
		return err
	}
	for _, fixture := range []struct{ name, file string }{{"acme-smoke", "acme_smoke-1.0.0-py3-none-any.whl"}, {"acme-source", "acme_source-1.0.0.tar.gz"}} {
		b, err := os.ReadFile(filepath.Join(w.dir, "dist", fixture.file))
		if err != nil {
			return err
		}
		artifactMedia := "application/octet-stream"
		if strings.HasSuffix(fixture.file, ".tar.gz") {
			artifactMedia = "binary/octet-stream" // Warehouse's observed source-archive label.
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(b))
		w.remote.Set(
			"/files/"+fixture.file,
			fakeupstream.Resource{
				Body:   b,
				Header: http.Header{"Content-Type": []string{artifactMedia}},
			},
		)
		f := map[string]any{
			"filename":        fixture.file,
			"url":             "../../files/" + fixture.file,
			"hashes":          map[string]string{"sha256": sum},
			"requires-python": ">=3.8",
			"yanked":          false,
		}
		attrs := ""
		if strings.HasSuffix(fixture.file, ".whl") {
			d, e := pypifmt.ReadDist(bytes.NewReader(b), int64(len(b)), fixture.file)
			if e != nil {
				return e
			}
			msum := fmt.Sprintf("%x", sha256.Sum256([]byte(d.Metadata)))
			f["core-metadata"] = map[string]string{"sha256": msum}
			attrs = ` data-core-metadata="sha256=` + msum + `" data-dist-info-metadata="sha256=` + msum + `"`
			w.remote.Set(
				"/files/"+fixture.file+".metadata",
				fakeupstream.Resource{
					Body:   []byte(d.Metadata),
					Header: http.Header{"Content-Type": []string{"application/octet-stream"}},
				},
			)
		}

		files := []any{f}
		extraHTML := ""
		if strings.HasSuffix(fixture.file, ".whl") {
			for _, version := range []string{"2.0.0", "3.0.0"} {
				filename := strings.Replace(fixture.file, "1.0.0", version, 1)
				extra := map[string]any{
					"filename":        filename,
					"url":             "../../files/" + filename,
					"hashes":          map[string]string{},
					"yanked":          version == "2.0.0",
					"requires-python": ">=3.8",
				}
				attributes := ` data-yanked="broken release"`
				if version == "3.0.0" {
					extra["requires-python"] = ">=99"
					attributes = ` data-requires-python="&gt;=99"`
				}
				files = append(files, extra)
				extraHTML += `<a href="../../files/` + filename + `"` + attributes + `>` + filename + `</a>`
			}
		}
		body, err := json.Marshal(
			map[string]any{
				"meta":  map[string]string{"api-version": "1.1"},
				"name":  fixture.name,
				"files": files,
			},
		)
		if err != nil {
			return err
		}
		media := "application/vnd.pypi.simple.v1+json"
		if form == "html" {
			body = []byte(
				`<!DOCTYPE html><html><body><a href="../../files/` + fixture.file + `#sha256=` + sum + `" data-requires-python="&gt;=3.8"` + attrs + `>` + fixture.file + `</a>` + extraHTML + `</body></html>`,
			)
			media = "text/html"
		}
		w.remote.Set(
			"/simple/"+fixture.name+"/",
			fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": []string{media}}},
		)
	}
	return nil
}

func (w *world) installRemotePython(client, env string) error {
	args := "--isolated --python " + env + " install --no-deps --no-cache-dir --index-url {authurl}/api/pypi/pypi-remote/simple/ acme-smoke<4 acme-source==1.0.0"
	if client == "uv" {
		args = "--no-config pip install --python " + env + "/bin/python --no-deps --no-cache --index-url {authurl}/api/pypi/pypi-remote/simple/ acme-smoke<4 acme-source==1.0.0"
	}
	return w.runPython(client, args)
}

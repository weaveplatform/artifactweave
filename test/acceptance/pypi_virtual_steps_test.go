package acceptance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
)

func (w *world) registerVirtualPyPI(sc *godog.ScenarioContext) {
	sc.Step(`^a virtual Python upstream serves dependent wheel and sdist in "([^"]*)"$`, w.virtualPythonUpstream)
	sc.Step(`^I install virtual Python packages with "([^"]*)" into "([^"]*)"$`, w.installVirtualPython)
}

func (w *world) virtualPythonUpstream(form string) error {
	if err := w.pythonUpstream(form); err != nil {
		return err
	}
	if err := w.writeWheel("acme-private", "1.0.0", "local"); err != nil {
		return err
	}
	for _, fixture := range []struct{ name, file string }{{"acme-smoke", "acme_smoke-1.0.0-py3-none-any.whl"}, {"acme-source", "acme_source-1.0.0.tar.gz"}} {
		path := filepath.Join(w.dir, "dist", fixture.file)
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var body []byte
		if strings.HasSuffix(path, ".whl") {
			body, err = dependentWheel(original)
		} else {
			body, err = dependentSdist(original)
		}
		if err != nil {
			return err
		}
		if err = os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
		w.remote.Set("/files/"+fixture.file, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/octet-stream"}}})
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		file := map[string]any{
			"filename":        fixture.file,
			"url":             "../../files/" + fixture.file,
			"hashes":          map[string]string{"sha256": sum},
			"requires-python": ">=3.8",
			"yanked":          false,
		}
		attrs := ""
		if strings.HasSuffix(path, ".whl") {
			dist, err := pypifmt.ReadDist(bytes.NewReader(body), int64(len(body)), fixture.file)
			if err != nil {
				return err
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(dist.Metadata)))
			file["core-metadata"] = map[string]string{"sha256": hash}
			attrs = ` data-core-metadata="sha256=` + hash + `"`
			w.remote.Set("/files/"+fixture.file+".metadata", fakeupstream.Resource{Body: []byte(dist.Metadata), Header: http.Header{"Content-Type": {"application/octet-stream"}}})
		}
		page := encodePythonPage(fixture.name, file)
		media := "application/vnd.pypi.simple.v1+json"
		if form == "html" {
			page = []byte(
				`<!DOCTYPE html><html><body><a href="../../files/` + fixture.file + `#sha256=` + sum + `" data-requires-python="&gt;=3.8"` + attrs + `>` + fixture.file + `</a></body></html>`,
			)
			media = "text/html"
		}
		w.remote.Set("/simple/"+fixture.name+"/", fakeupstream.Resource{Body: page, Header: http.Header{"Content-Type": {media}}})
	}
	w.remote.Set(
		"/simple/",
		fakeupstream.Resource{
			Body:   []byte(`<!DOCTYPE html><html><body><a href="acme-smoke/">acme-smoke</a><a href="acme-source/">acme-source</a></body></html>`),
			Header: http.Header{"Content-Type": {"text/html"}},
		},
	)
	return nil
}

func encodePythonPage(name string, file map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"meta": map[string]string{"api-version": "1.1"}, "name": name, "files": []any{file}})
	return b
}

func dependentMetadata(body []byte, requirements ...string) []byte {
	return bytes.Replace(body, []byte("\n\n"), []byte("\nRequires-Dist: "+strings.Join(requirements, "\nRequires-Dist: ")+"\n\n"), 1)
}

func dependentWheel(body []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	recordName := ""
	var record strings.Builder
	for _, file := range reader.File {
		if strings.HasSuffix(file.Name, ".dist-info/RECORD") {
			recordName = file.Name
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(file.Name, ".dist-info/METADATA") {
			data = dependentMetadata(data, "acme-private==1.0.0", "acme-source==1.0.0")
		}
		entry, err := writer.Create(file.Name)
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(data); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", file.Name, base64.RawURLEncoding.EncodeToString(sum[:]), len(data))
	}
	entry, err := writer.Create(recordName)
	if err != nil {
		return nil, err
	}
	if _, err = io.WriteString(entry, record.String()+recordName+",,\n"); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func dependentSdist(body []byte) ([]byte, error) {
	input, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer input.Close()
	reader := tar.NewReader(input)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	writer := tar.NewWriter(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(header.Name, "/PKG-INFO") {
			data = dependentMetadata(data, "acme-private==1.0.0")
		}
		header.Size = int64(len(data))
		if err = writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err = writer.Write(data); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (w *world) installVirtualPython(client, env string) error {
	args := "--isolated --python " + env + " install --retries 0 --no-cache-dir --index-url {authurl}/api/pypi/pypi-virtual/simple/ acme-smoke==1.0.0"
	if client == "uv" {
		args = "--no-config pip install --python " + env + "/bin/python --no-cache --index-url {authurl}/api/pypi/pypi-virtual/simple/ acme-smoke==1.0.0"
	}
	return w.runPython(client, args)
}

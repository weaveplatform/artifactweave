package acceptance

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerTerraformRemote(sc *godog.ScenarioContext) {
	sc.Step(
		`^a Terraform registry upstream serves versioned ZIP and subdirectory archives$`,
		w.terraformRemoteUpstream,
	)
	sc.Step(
		`^the Terraform upstream received cold module discovery and archive requests$`,
		w.terraformRemoteRequests,
	)
}

func (w *world) terraformRemoteUpstream() error {
	if err := w.remoteUpstream("/ready", "ready"); err != nil {
		return err
	}
	putJSON := func(path string, doc any) {
		b, _ := json.Marshal(doc)
		w.remote.Set(
			path,
			fakeupstream.Resource{
				Body:   b,
				Header: http.Header{"Content-Type": {"application/json"}},
			},
		)
	}
	putJSON("/.well-known/terraform.json", map[string]string{"modules.v1": "/registry/modules/"})
	putJSON(
		"/registry/modules/acme/greeting/aws/versions",
		map[string]any{
			"modules": []any{
				map[string]any{
					"versions": []any{
						map[string]string{"version": "1.0.0"},
						map[string]string{"version": "2.0.0"},
					},
				},
			},
		},
	)
	var zipBody bytes.Buffer
	z := zip.NewWriter(&zipBody)
	f, err := z.Create("main.tf")
	if err != nil {
		return err
	}
	if _, err = f.Write([]byte("output \"release\" { value = \"cold ZIP 1.0.0\" }\n")); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	w.remote.Set(
		"/archives/release.zip",
		fakeupstream.Resource{
			Body:   zipBody.Bytes(),
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	w.remote.Set(
		"/registry/modules/acme/greeting/aws/1.0.0/download",
		fakeupstream.Resource{
			Status: 204,
			Header: http.Header{"X-Terraform-Get": {"/archives/release.zip"}},
		},
	)
	var tarBody bytes.Buffer
	g := gzip.NewWriter(&tarBody)
	tw := tar.NewWriter(g)
	files := []struct{ name, body string }{
		{
			"snapshot/modules/demo/main.tf",
			"module \"helper\" { source = \"../helper\" }\noutput \"release\" { value = module.helper.release }\n",
		},
		{
			"snapshot/modules/helper/main.tf",
			"output \"release\" { value = \"nested tar 2.0.0\" }\n",
		},
		{"snapshot/README.md", "complete source package preserved\n"},
	}
	for _, file := range files {
		if err = tw.WriteHeader(
			&tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.body))},
		); err != nil {
			return err
		}
		if _, err = tw.Write([]byte(file.body)); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = g.Close(); err != nil {
		return err
	}
	w.remote.Set(
		"/archives/nested",
		fakeupstream.Resource{
			Body:   tarBody.Bytes(),
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		},
	)
	// The final response base, not the original download endpoint, resolves this
	// relative archive link; the wildcard remains native getter syntax.
	w.remote.Set(
		"/registry/modules/acme/greeting/aws/2.0.0/download",
		fakeupstream.Resource{Redirect: "/actual/location"},
	)
	w.remote.Set(
		"/actual/location",
		fakeupstream.Resource{
			Status: 204,
			Header: http.Header{
				"X-Terraform-Get": {"../archives/nested//*/modules/demo?archive=tar.gz"},
			},
		},
	)
	return nil
}

func (w *world) terraformRemoteRequests() error {
	for _, path := range []string{"/.well-known/terraform.json", "/registry/modules/acme/greeting/aws/versions", "/registry/modules/acme/greeting/aws/1.0.0/download", "/actual/location", "/archives/release.zip", "/archives/nested"} {
		if n := w.remote.Count("GET", path); n < 1 {
			return fmt.Errorf("Terraform did not fetch %s through controlled upstream", path)
		}
	}
	return nil
}

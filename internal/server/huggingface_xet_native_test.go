package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccHuggingFaceNativeXet(t *testing.T) {
	python := os.Getenv("HF_XET_PYTHON")
	if python == "" {
		t.Skip("HF_XET_PYTHON enables the pinned native hf_xet client")
	}
	w := newItemWorld(t, false, func(c *Config) {
		c.HuggingFaceXetEnabled = true
		c.Keys = support.TerraformBackendKeys(t)
		c.BaseURL = ""
	})
	for _, c := range []repo.Config{{Key: "hf", Class: repo.Local, PackageType: repo.HuggingFaceML}, {Key: "hf-v", Class: repo.Virtual, PackageType: repo.HuggingFaceML, Repositories: []string{"hf"}, DefaultDeploymentRepo: "hf"}} {
		if err := w.db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	var uploads, downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/resolve/") && r.Method == http.MethodGet {
			t.Error("native Xet download fell back to Hub HTTP")
			out.WriteHeader(500)
			return
		}
		if strings.Contains(r.URL.Path, "/v1/xorbs/") && r.Method == http.MethodPost {
			uploads.Add(1)
		}
		if strings.Contains(r.URL.Path, "/data/") {
			downloads.Add(1)
		}
		w.h.ServeHTTP(out, r)
	}))
	defer srv.Close()
	cmd := exec.CommandContext(
		t.Context(),
		python,
		"-c",
		nativeXetRoundTrip,
		srv.URL+"/artifactory/api/huggingfaceml/hf-v",
		t.TempDir(),
	)
	cmd.Env = append(
		os.Environ(),
		"HF_HUB_DISABLE_XET=0",
		"HF_HUB_DISABLE_TELEMETRY=1",
		"HF_HOME="+t.TempDir(),
		"HF_XET_CACHE="+t.TempDir(),
		"HF_HUB_DISABLE_PROGRESS_BARS=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native hf_xet: %v\n%s", err, out)
	}
	if uploads.Load() == 0 || downloads.Load() == 0 {
		t.Fatalf(
			"native CAS upload/download counts %d/%d: %s",
			uploads.Load(),
			downloads.Load(),
			out,
		)
	}
}

const nativeXetRoundTrip = `
import base64, pathlib, random, sys
from importlib.metadata import version
from huggingface_hub import HfApi, hf_hub_download
assert version("huggingface_hub") == "2.3.0"
assert version("hf_xet") == "1.7.0"
endpoint, root = sys.argv[1], pathlib.Path(sys.argv[2])
headers = {"Authorization": "Basic " + base64.b64encode(b"admin:pw").decode()}
api = HfApi(endpoint=endpoint,token=False,headers=headers)
for kind in ["model","dataset"]:
    api.create_repo("acme/demo",repo_type=kind,exist_ok=True)
    source=root/(kind+".bin")
    source.write_bytes(random.Random(779).randbytes(1200000))
    api.upload_file(path_or_fileobj=source,path_in_repo="weights.bin",repo_id="acme/demo",repo_type=kind)
    result=hf_hub_download("acme/demo","weights.bin",repo_type=kind,endpoint=endpoint,token=False,headers=headers,cache_dir=root/(kind+"-cache"))
    assert pathlib.Path(result).read_bytes()==source.read_bytes()
print("native Hub 2.3.0 / hf_xet 1.7.0 model and dataset round trips passed")
`

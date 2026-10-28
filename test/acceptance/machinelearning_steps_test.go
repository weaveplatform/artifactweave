package acceptance

import (
	"os"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
)

func (w *world) registerMachineLearning(sc *godog.ScenarioContext) {
	sc.Step(`^I publish the controlled FrogML model to "([^"]*)"$`, w.publishFrogML)
	sc.Step(
		`^I download the controlled FrogML model from "([^"]*)" with a fresh client$`,
		w.downloadFrogML,
	)
	sc.Step(
		`^I try FrogML (publish|download) with invalid credentials against "([^"]*)"$`,
		w.deniedFrogML,
	)
}

func (w *world) runFrogML(key, token, script string) error {
	base := w.url
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_FROGML_DESKTOP") == "1" {
		base = strings.Replace(base, "127.0.0.1", "host.docker.internal", 1)
	}
	return w.runContainer(w.frogMLImage, []string{
		"-e", "JF_URL=" + base + "/artifactory",
		"-e", "JF_ACCESS_TOKEN=" + token,
		"-e", "HOME=/tmp/frogml-home",
		"-e", "MODEL_REPOSITORY=" + key,
	}, []string{"python", "-c", script})
}

const frogMLPublish = `
import os
from pathlib import Path
from frogml import files
source = Path('/work/model.pkl')
source.write_bytes(b'controlled model bytes\n' * 100000)
files.log_model(str(source), os.environ['MODEL_REPOSITORY'], 'controlled-model',
                version='1.0.0', properties={'purpose': 'acceptance'}, register_in_jml=False)
`

const frogMLDownload = `
import hashlib, os, shutil
from pathlib import Path
from frogml import files
repo = os.environ['MODEL_REPOSITORY']
info = files.get_model_info(repo, 'controlled-model', '1.0.0')
assert info['model_artifacts'][0]['checksums']['sha2'] == hashlib.sha256(Path('/work/model.pkl').read_bytes()).hexdigest()
target = Path('/work/downloaded-model')
shutil.rmtree(target, ignore_errors=True)
files.load_model(repo, 'controlled-model', '1.0.0', str(target))
assert (target / 'model.pkl').read_bytes() == Path('/work/model.pkl').read_bytes()
print('FrogML model bytes and SHA-256 verified')
`

func (w *world) publishFrogML(key string) error {
	return w.runFrogML(key, w.vars["frogml-token"], frogMLPublish)
}

func (w *world) downloadFrogML(key string) error {
	return w.runFrogML(key, w.vars["frogml-token"], frogMLDownload)
}

func (w *world) deniedFrogML(operation, key string) error {
	script := frogMLDownload
	if operation == "publish" {
		script = frogMLPublish
	}
	return w.runFrogML(key, "invalid-acceptance-credential", script)
}

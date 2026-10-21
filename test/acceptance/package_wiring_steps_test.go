package acceptance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	_ "modernc.org/sqlite" // Observe the running binary's queue without opening a writable store.

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

type wiringPackage struct {
	file, item, version, index, download string
}

var wiringPackages = map[string]wiringPackage{
	"PyPI": {
		"dist/acme_wire-1.0.0-py3-none-any.whl", "uploads/acme_wire-1.0.0-py3-none-any.whl", "1.0.0",
		"api/pypi/%s/simple/acme-wire/", "api/pypi/%s/uploads/acme_wire-1.0.0-py3-none-any.whl",
	},
	"NuGet": {
		"dist/Acme.Wire.1.0.0.nupkg", "Acme.Wire.1.0.0.nupkg", "1.0.0",
		"api/nuget/v3/%s/flatcontainer/acme.wire/index.json",
		"api/nuget/v3/%s/flatcontainer/acme.wire/1.0.0/acme.wire.1.0.0.nupkg",
	},
	"Helm": {
		"dist/acme-wire-0.1.0.tgz", "acme-wire-0.1.0.tgz", "0.1.0",
		"api/helm/%s/index.yaml", "api/helm/%s/acme-wire-0.1.0.tgz",
	},
	"Terraform module": {
		"dist/module.zip", "acme/wire/aws/1.0.0.zip", "1.0.0",
		"api/terraform/%s/v1/modules/acme/wire/aws/versions",
		"api/terraform/%s/v1/modules/acme/wire/aws/1.0.0/download",
	},
	"Terraform provider": {
		"dist/provider.zip", "acme/wire/1.0.0/terraform-provider-wire_1.0.0_linux_amd64.zip", "1.0.0",
		"api/terraform/%s/providers/private.example/acme/wire/index.json",
		"api/terraform/%s/providers/private.example/acme/wire/1.0.0.json",
	},
}

func (w *world) registerPackageWiring(sc *godog.ScenarioContext) {
	sc.Step(`^a "([^"]*)" package wiring fixture$`, w.packageWiringFixture)
	sc.Step(`^the "([^"]*)" reads in "([^"]*)" serve the wiring package$`, w.wiringReads)
	sc.Step(`^the "([^"]*)" reads in "([^"]*)" hide the wiring package$`, w.wiringAbsent)
	sc.Step(`^the queued reindex of "([^"]*)" completes within (\d+) seconds$`, w.wiringReindexComplete)
}

func (w *world) packageWiringFixture(kind string) error {
	p, ok := wiringPackages[kind]
	if !ok {
		return fmt.Errorf("unknown wiring fixture %q", kind)
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["wiring-item"] = p.item
	switch kind {
	case "PyPI":
		return w.writeWheel("acme-wire", p.version, "dist")
	case "NuGet":
		return w.writeNupkg("Acme.Wire", p.version, "dist")
	case "Helm":
		if err := w.binaryAvailable("helm"); err != nil {
			return err
		}
		for _, command := range []string{"create acme-wire", "package acme-wire --destination dist"} {
			if err := w.runHelm(command); err != nil {
				return err
			}
			if err := w.commandSucceeds(); err != nil {
				return err
			}
		}
		return nil
	case "Terraform module":
		return w.writeFile(p.file, &godog.DocString{Content: string(terraformtest.ModuleZip(
			map[string]string{"main.tf": `output "greeting" { value = "wired" }`},
		))})
	default:
		return w.writeFile(p.file, &godog.DocString{Content: string(
			terraformtest.ProviderZip("wire", p.version, "linux_amd64"),
		)})
	}
}

// Read both the index and the archive through the format. Terraform's download
// metadata supplies a signed URL; following that actual URL also catches a copy
// that accidentally keeps pointing at the source repository.
func (w *world) wiringReads(kind, repo string) error {
	p := wiringPackages[kind]
	if err := w.getAsAdmin("/artifactory/" + fmt.Sprintf(p.index, repo)); err != nil {
		return err
	}
	if err := w.statusIs(http.StatusOK); err != nil {
		return err
	}
	if err := w.bodyContains(p.version); err != nil {
		return err
	}
	if err := w.getAsAdmin("/artifactory/" + fmt.Sprintf(p.download, repo)); err != nil {
		return err
	}
	switch kind {
	case "Terraform module":
		if err := w.statusIs(http.StatusNoContent); err != nil {
			return err
		}
		if err := w.keepHeaderPath("X-Terraform-Get", "wiring-download"); err != nil {
			return err
		}
	case "Terraform provider":
		if err := w.statusIs(http.StatusOK); err != nil {
			return err
		}
		var mirror struct {
			Archives map[string]struct {
				URL string `json:"url"`
			} `json:"archives"`
		}
		if err := json.Unmarshal(w.body, &mirror); err != nil {
			return err
		}
		w.vars["wiring-download"] = mirror.Archives["linux_amd64"].URL
	}
	if strings.HasPrefix(kind, "Terraform ") {
		u, err := url.Parse(w.vars["wiring-download"])
		if err != nil {
			return err
		}
		if !strings.Contains(u.Path, "/"+repo+"/") {
			return fmt.Errorf("download path %q does not name repository %q", u.Path, repo)
		}
		if err := w.get(u.RequestURI()); err != nil {
			return err
		}
	}
	if err := w.statusIs(http.StatusOK); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	want, err := os.ReadFile(filepath.Join(dir, p.file))
	if err != nil {
		return err
	}
	if !bytes.Equal(w.body, want) {
		return fmt.Errorf("%s archive from %s differs from REST-deployed fixture", kind, repo)
	}
	return nil
}

func (w *world) wiringAbsent(kind, repo string) error {
	p := wiringPackages[kind]
	if err := w.getAsAdmin("/artifactory/" + fmt.Sprintf(p.index, repo)); err != nil {
		return err
	}
	want := http.StatusNotFound
	if kind == "Helm" {
		want = http.StatusOK
	}
	if err := w.statusIs(want); err != nil {
		return err
	}
	if err := w.outputLacks(p.version); err != nil {
		return err
	}
	if err := w.getAsAdmin("/artifactory/" + fmt.Sprintf(p.download, repo)); err != nil {
		return err
	}
	return w.statusIs(http.StatusNotFound)
}

// Reindex is asynchronous and Terraform has no derived properties to poll. The
// scenario observes only this default SQLite queue, read-only, after HTTP has
// accepted the work. An absent row means successful completion; retries and
// parked work retain their diagnostics. No artifact or index is changed here.
func (w *world) wiringReindexComplete(repo string, seconds int) error {
	u := url.URL{Scheme: "file", Path: filepath.Join(w.dataDir, "artifactweave.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	for {
		var state, lastError string
		var attempts int
		err := db.QueryRowContext(ctx,
			"SELECT state, attempts, last_error FROM work_items WHERE kind = ? AND key = ?",
			format.KindReindex, repo).Scan(&state, &attempts, &lastError)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("observe reindex of %s: %w", repo, err)
		}
		if state == "parked" {
			return fmt.Errorf("reindex of %s parked after %d attempts: %s", repo, attempts, lastError)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("reindex of %s timed out: state=%s attempts=%d error=%s",
				repo, state, attempts, lastError)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

const vcsFixtureOID = "0123456789abcdef0123456789abcdef01234567"

func (w *world) registerVCS(sc *godog.ScenarioContext) {
	sc.Step(`^a VCS Git upstream serves a tagged project with marker "([^"]*)"$`, w.vcsUpstream)
	sc.Step(`^the VCS archive contains marker "([^"]*)"$`, w.vcsArchiveContains)
}

func (w *world) vcsUpstream(marker string) error {
	if err := w.remoteUpstream("/ready", "VCS fixture"); err != nil {
		return err
	}
	ref := vcsFixtureOID + " refs/tags/v1.0.0\n"
	w.remote.Set("/git/team/project.git/info/refs", fakeupstream.Resource{
		Body: []byte(
			"001e# service=git-upload-pack\n0000" + fmt.Sprintf("%04x%s", len(ref)+4, ref) + "0000",
		),
		Header: http.Header{"Content-Type": {"application/x-git-upload-pack-advertisement"}},
	})
	archive, err := bowerGitArchive("project", "1.0.0", marker)
	if err != nil {
		return err
	}
	w.remote.Set("/git/team/project/archive/"+vcsFixtureOID+".tar.gz",
		fakeupstream.Resource{Body: archive})
	return nil
}

func (w *world) vcsArchiveContains(marker string) error {
	gz, err := gzip.NewReader(bytes.NewReader(w.body))
	if err != nil {
		return fmt.Errorf("VCS response is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("VCS archive has no marker %q", marker)
		}
		if err != nil {
			return err
		}
		if !strings.HasSuffix(hdr.Name, "/index.js") {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 64<<10))
		if err != nil {
			return err
		}
		if !bytes.Contains(body, []byte(marker)) {
			return fmt.Errorf("VCS archive marker missing from %s", hdr.Name)
		}
		return nil
	}
}

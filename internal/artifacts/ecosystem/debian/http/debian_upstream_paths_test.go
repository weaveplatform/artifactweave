package debianhttp

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
)

// The paths of #1273's table that a remote repository answered not found
// without asking its upstream, and the translation it already relayed.
const (
	debInstallerIndex = "dists/stable/main/debian-installer/binary-amd64/Packages.gz"
	debLzmaIndex      = "dists/stable/main/binary-amd64/Packages.lzma"
	debPoolByHash     = "pool/main/by-hash/SHA256/alpha_1.0_amd64.deb"
)

// indexForm is index in the form suffix names, as a repository writes it.
func indexForm(t *testing.T, index []byte, suffix string) []byte {
	t.Helper()
	forms, err := debian.IndexForms(index, []string{"bz2", "lzma", "xz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range forms {
		if form.Suffix == suffix {
			return form.Body
		}
	}
	t.Fatalf("no %q form", suffix)
	return nil
}

// TestAccDebianRemoteAsksItsUpstreamForWhatAptReads: a remote repository
// asks its upstream for each path of #1273's table and serves the
// upstream's bytes: an installer's Packages index, a translation's and a
// Contents index's by-hash copies, a .lzma index and a package under a
// folder named by-hash; it answered each 404 before without asking. A
// translation was relayed before and still is.
func TestAccDebianRemoteAsksItsUpstreamForWhatAptReads(t *testing.T) {
	udeb := strings.Replace(string(packagesIndex("alpha 1.0 "+debAlpha)), debAlpha,
		"pool/alpha_1.0_amd64.udeb", 1)
	translation := []byte("translation bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(translation))
	files := map[string][]byte{
		debInstallerIndex: indexForm(t, []byte(udeb), ".gz"),
		"dists/stable/main/i18n/by-hash/SHA256/" + sum: translation,
		"dists/stable/main/by-hash/SHA256/" + sum:      translation,
		debLzmaIndex:   indexForm(t, packagesIndex("alpha 1.0 "+debAlpha), ".lzma"),
		debPoolByHash:  debBody,
		debTranslation: []byte("translation file"),
	}
	at := map[string]func() (source.Result, error){}
	for path, body := range files {
		at[path] = streamed(body, nil)
	}
	for path, body := range files {
		t.Run(path, func(t *testing.T) {
			asked := false
			w := newOverlayWorld(t, withSources(
				func(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
					asked = asked || q.Resource.Path == path
					return upstream(at)(ctx, q, p)
				},
			))
			w.debRepos(repo.Config{Key: "deb-remote", Class: repo.Remote})
			rec := w.get("deb-remote", path, "admin")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
				t.Errorf("answered %d %q, want 200 and the upstream's bytes", rec.Code, rec.Body)
			}
			if !asked {
				t.Error("the upstream was not asked")
			}
		})
	}
}

// TestAccDebianVirtualServesItsRemoteMembersInstallerIndex: a virtual
// repository whose remote member is listed first serves that member's copy
// of an installer's Packages index, where it passed over the member to a
// later local member's copy before #1273.
func TestAccDebianVirtualServesItsRemoteMembersInstallerIndex(t *testing.T) {
	remote, local := []byte("remote installer index"), []byte("local installer index")
	w := newOverlayWorld(t, withSources(upstream(map[string]func() (source.Result, error){
		debInstallerIndex: streamed(remote, nil),
	})))
	w.debRepos(
		repo.Config{Key: "deb-remote", Class: repo.Remote},
		repo.Config{Key: "deb-local", Class: repo.Local},
		repo.Config{
			Key: "deb-virtual", Class: repo.Virtual,
			Repositories: []string{"deb-remote", "deb-local"},
		},
	)
	w.put("deb-local", debInstallerIndex, false, local)
	rec := w.get("deb-virtual", debInstallerIndex, "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), remote) {
		t.Errorf("answered %d %q, want 200 and the remote member's copy", rec.Code, rec.Body)
	}
}

// decompress reads body back in the form suffix names.
func decompress(t *testing.T, suffix string, body []byte) []byte {
	t.Helper()
	var r io.Reader
	var err error
	switch suffix {
	case ".bz2":
		r = bzip2.NewReader(bytes.NewReader(body))
	case ".lzma":
		r, err = lzma.NewReader(bytes.NewReader(body))
	case ".xz":
		r, err = xz.NewReader(bytes.NewReader(body))
	default:
		return body
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAccDebianVirtualComposesItsOptionalIndexForms: a virtual repository
// configured with bz2, lzma and xz composes its merged Packages index in
// each, beside Packages and Packages.gz, each decompressing to the merged
// index, lists each in its Release and serves each by hash (#1274). The
// virtual repository's own setting decides, not its member's. Without the
// setting it composes Packages and Packages.gz alone, and a GET of another
// form is not found, as before.
func TestAccDebianVirtualComposesItsOptionalIndexForms(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(
		repo.Config{
			Key: "deb-local", Class: repo.Local,
			OptionalIndexCompressionFormats: []string{"bz2"},
		},
		repo.Config{
			Key: "deb-forms", Class: repo.Virtual, Repositories: []string{"deb-local"},
			OptionalIndexCompressionFormats: []string{"xz", "lzma", "bz2"},
		},
		repo.Config{Key: "deb-plain", Class: repo.Virtual, Repositories: []string{"deb-local"}},
	)
	w.publish("deb-local", packagesIndex("alpha 1.0 "+debAlpha))
	plain := w.get("deb-forms", debPackages, "admin")
	if plain.Code != http.StatusOK {
		t.Fatalf("Packages answered %d %s", plain.Code, plain.Body)
	}
	release := w.get("deb-forms", debRelease, "admin").Body.String()
	for _, suffix := range []string{".bz2", ".lzma", ".xz"} {
		rec := w.get("deb-forms", debPackages+suffix, "admin")
		if rec.Code != http.StatusOK {
			t.Errorf("Packages%s answered %d %s", suffix, rec.Code, rec.Body)
			continue
		}
		if got := decompress(t, suffix, rec.Body.Bytes()); !bytes.Equal(got, plain.Body.Bytes()) {
			t.Errorf("Packages%s decompresses to %q", suffix, got)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(rec.Body.Bytes()))
		row := fmt.Sprintf(" %s %16d main/binary-amd64/Packages%s\n", sum, rec.Body.Len(), suffix)
		if !strings.Contains(release, row) {
			t.Errorf("Release does not list Packages%s as %q:\n%s", suffix, row, release)
		}
		byHash := w.get("deb-forms", "dists/stable/main/binary-amd64/by-hash/SHA256/"+sum, "admin")
		if byHash.Code != http.StatusOK || !bytes.Equal(byHash.Body.Bytes(), rec.Body.Bytes()) {
			t.Errorf("Packages%s by hash answered %d", suffix, byHash.Code)
		}
	}
	release = w.get("deb-plain", debRelease, "admin").Body.String()
	for _, suffix := range []string{".bz2", ".lzma", ".xz"} {
		wantError(t, w.get("deb-plain", debPackages+suffix, "admin"),
			http.StatusNotFound, "Could not find resource")
		if strings.Contains(release, "Packages"+suffix) {
			t.Errorf("a virtual repository without the setting lists Packages%s", suffix)
		}
	}
	if !strings.Contains(release, "main/binary-amd64/Packages.gz\n") {
		t.Errorf("Release without the setting lost Packages.gz:\n%s", release)
	}
}

// TestAccDebianLocalServesAnLzmaIndexAsAnIndex: a local repository's
// Packages.lzma, which it writes when configured for it, is read as an
// index, as Packages.xz and Packages.bz2 are: whole, and only by a caller
// who may read every package it lists.
func TestAccDebianLocalServesAnLzmaIndexAsAnIndex(t *testing.T) {
	w := newOverlayWorld(t)
	w.debRepos(repo.Config{Key: "deb-local", Class: repo.Local})
	index := indexForm(t, packagesIndex("alpha 1.0 "+debAlpha), ".lzma")
	w.put("deb-local", debLzmaIndex, false, index)
	rec := w.get("deb-local", debLzmaIndex, "admin")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) ||
		rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("answered %d %q %q", rec.Code, rec.Header(), rec.Body)
	}
	indexOnly := &accessapitest.Decider{Admin: true, AllowsFunc: func(
		_ accessapi.ResourceType, _ accessapi.Repo, path string, _ accessapi.Actions,
	) bool {
		return !strings.HasPrefix(path, "pool/")
	}}
	wantError(t, w.getAs("deb-local", debLzmaIndex, indexOnly), http.StatusForbidden, "Forbidden")
}

package acceptance

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format/nuget"
)

// The last response is the .snupkg produced and pushed by the real SDK.
// Unit tests independently assert the GUID/age byte layout; this assertion
// proves the client-generated PDB is extracted unchanged and routed by key.
func (w *world) nugetSymbolsResolve(repo string) error {
	zr, err := zip.NewReader(bytes.NewReader(w.body), int64(len(w.body)))
	if err != nil {
		return fmt.Errorf("read client symbols package: %w", err)
	}
	var urls []string
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".pdb") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open client PDB: %w", err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("read client PDB: %w", err)
		}
		key, ok := nuget.PDBKey(body)
		if !ok {
			return fmt.Errorf("client PDB %s has no supported identity", f.Name)
		}
		name := url.PathEscape(path.Base(f.Name))
		u := "/artifactory/api/nuget/v3/" + url.PathEscape(
			repo,
		) + "/" + name + "/" + strings.ToLower(
			key,
		) + "/" + name
		if err := w.getAsAdmin(u); err != nil {
			return err
		}
		if err := w.statusIs(200); err != nil {
			return err
		}
		if !bytes.Equal(w.body, body) {
			return fmt.Errorf("symbol-server bytes differ from client PDB %s", f.Name)
		}
		urls = append(urls, u)
	}
	if len(urls) == 0 {
		return fmt.Errorf("client symbols package contained no PDB")
	}
	w.vars["nuget-symbol-urls"] = strings.Join(urls, "\n")
	return nil
}

func (w *world) nugetSymbolsRemoved() error {
	urls := w.vars["nuget-symbol-urls"]
	if urls == "" {
		return fmt.Errorf("no symbol-server URLs were captured")
	}
	for _, u := range strings.Split(urls, "\n") {
		if err := w.getAsAdmin(u); err != nil {
			return err
		}
		if err := w.statusIs(404); err != nil {
			return err
		}
	}
	return nil
}

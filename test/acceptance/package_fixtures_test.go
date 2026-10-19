package acceptance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

// The P8 packages the scenarios build themselves: a wheel and a nupkg are
// zip archives with metadata the clients read, and building them here keeps
// a scenario off the network a build backend or dotnet pack would reach.
// Helm charts need no such help: helm create and helm package make them
// offline.

// archiveFile is one entry of a zip a scenario builds.
type archiveFile struct{ name, content string }

// wheelNameSeparators are the runs a wheel's file name folds to "_" (the
// binary distribution format's escaping of a project name).
var wheelNameSeparators = regexp.MustCompile(`[-_.]+`)

// writeWheel writes a pure-Python wheel for name and version into dir, as
// <name>-<version>-py3-none-any.whl with the name folded. Its package holds
// one module, and its dist-info the METADATA, WHEEL and RECORD every
// installer reads, RECORD with each file's SHA-256 as the format requires.
func (w *world) writeWheel(name, version, dir string) error {
	folded := strings.ToLower(wheelNameSeparators.ReplaceAllString(name, "_"))
	info := folded + "-" + version + ".dist-info"
	files := []archiveFile{
		{folded + "/__init__.py", "GREETING = \"hello from artifactweave\"\n"},
		{info + "/METADATA", "Metadata-Version: 2.4\n" +
			"Name: " + name + "\n" +
			"Version: " + version + "\n" +
			"Summary: The acceptance features' wheel\n" +
			"Requires-Python: >=3.8\n" +
			"Description-Content-Type: text/markdown\n\n" +
			"# " + name + "\n\nA wheel the acceptance features build.\n"},
		{info + "/WHEEL", "Wheel-Version: 1.0\n" +
			"Generator: artifactweave-acceptance\n" +
			"Root-Is-Purelib: true\n" +
			"Tag: py3-none-any\n"},
	}
	var record strings.Builder
	for _, f := range files {
		sum := sha256.Sum256([]byte(f.content))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n",
			f.name, base64.RawURLEncoding.EncodeToString(sum[:]), len(f.content))
	}
	record.WriteString(info + "/RECORD,,\n")
	files = append(files, archiveFile{info + "/RECORD", record.String()})
	return w.writeArchive(path.Join(dir, folded+"-"+version+"-py3-none-any.whl"), files)
}

// writeNupkg writes a NuGet package for id and version into dir, as
// <id>.<version>.nupkg, the name dotnet pack gives it. It holds the nuspec
// at its root, which is all NuGet reads to restore it, and the OPC content
// types every nupkg carries. With no lib folder it suits any target
// framework.
func (w *world) writeNupkg(id, version, dir string) error {
	nuspec := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>` + id + `</id>
    <version>` + version + `</version>
    <authors>artifactweave</authors>
    <description>A package the acceptance features build.</description>
  </metadata>
</package>
`
	contentTypes := `<?xml version="1.0" encoding="utf-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="nuspec" ContentType="application/octet" /></Types>
`
	return w.writeArchive(path.Join(dir, id+"."+version+".nupkg"), []archiveFile{
		{id + ".nuspec", nuspec},
		{"[Content_Types].xml", contentTypes},
	})
}

// zipTree writes a zip of every file under a scenario directory, named by
// its path below it, such as a Terraform module's.
func (w *world) zipTree(name, root string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	base := filepath.Join(dir, root)
	var files []archiveFile
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, archiveFile{filepath.ToSlash(rel), string(content)})
		return nil
	})
	if err != nil {
		return err
	}
	return w.writeArchive(name, files)
}

// writeArchive writes a zip of the files, in order, to a scenario file.
func (w *world) writeArchive(name string, files []archiveFile) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := zw.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte(f.content)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return w.writeFile(name, &godog.DocString{Content: buf.String()})
}

// Package p2 validates Eclipse p2 update-site paths and bounded metadata.
package p2

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/ulikunitz/xz"
)

const (
	MaxMetadata = 36 << 20
	MaxArtifact = 512 << 20
)

var ErrInvalid = errors.New("invalid p2 repository content")

// Kind classifies the small set of native p2 files. An update site may place
// these under an archive suffix (site.zip!/content.jar) or a site directory.
type Kind uint8

const (
	Invalid Kind = iota
	Index
	Metadata
	Artifact
)

func Classify(name string) Kind {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00\r\n?#") {
		return Invalid
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return Invalid
		}
	}
	base := path.Base(name)
	switch base {
	case "p2.index":
		return Index
	case "content.xml", "content.jar", "compositeContent.xml", "compositeContent.jar",
		"artifacts.xml", "artifacts.jar", "compositeArtifacts.xml", "compositeArtifacts.jar",
		"content.xml.xz", "artifacts.xml.xz", "compositeContent.xml.xz", "compositeArtifacts.xml.xz":
		return Metadata
	}
	if strings.HasPrefix(name, "plugins/") || strings.HasPrefix(name, "features/") ||
		strings.HasPrefix(name, "binary/") || strings.Contains(name, "/plugins/") ||
		strings.Contains(name, "/features/") || strings.Contains(name, "/binary/") {
		if strings.HasPrefix(name, "binary/") || strings.Contains(name, "/binary/") {
			return Artifact
		}
		if strings.HasSuffix(base, ".jar") {
			return Artifact
		}
	}
	return Invalid
}

// Validate reads the complete representation before the source cache may
// publish it. zip readers check both directory structure and entry CRCs.
func Validate(name string, src io.ReadSeeker, size int64) error {
	kind := Classify(name)
	limit := int64(MaxMetadata)
	if kind == Artifact {
		limit = MaxArtifact
	}
	if kind == Invalid || size <= 0 || size > limit {
		return ErrInvalid
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return ErrInvalid
	}
	if kind == Index {
		return validateIndex(src, size)
	}
	if strings.HasSuffix(name, ".xml.xz") {
		r, err := xz.NewReader(src)
		if err != nil {
			return ErrInvalid
		}
		body, err := io.ReadAll(io.LimitReader(r, MaxMetadata+1))
		if err != nil || len(body) > MaxMetadata {
			return ErrInvalid
		}
		return validateXML(strings.TrimSuffix(path.Base(name), ".xz"), body)
	}
	if strings.HasPrefix(name, "binary/") || strings.Contains(name, "/binary/") {
		return nil
	}
	if strings.HasSuffix(name, ".jar") {
		return validateJAR(name, kind, src, size)
	}
	body, err := io.ReadAll(io.LimitReader(src, MaxMetadata+1))
	if err != nil || int64(len(body)) != size {
		return ErrInvalid
	}
	return validateXML(path.Base(name), body)
}

func validateJAR(name string, kind Kind, src io.ReadSeeker, size int64) error {
	reader, ok := src.(io.ReaderAt)
	if !ok {
		reader = seekReaderAt{src}
	}
	z, err := zip.NewReader(reader, size)
	if err != nil || len(z.File) == 0 || len(z.File) > 4096 {
		return ErrInvalid
	}
	var total uint64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if total > MaxArtifact || f.UncompressedSize64 > MaxArtifact {
			return ErrInvalid
		}
		r, err := f.Open()
		if err != nil {
			return ErrInvalid
		}
		_, err = io.Copy(io.Discard, io.LimitReader(r, int64(f.UncompressedSize64)+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			return ErrInvalid
		}
	}
	if kind != Metadata {
		return nil
	}
	entry := strings.TrimSuffix(path.Base(name), ".jar") + ".xml"
	for _, f := range z.File {
		if f.Name != entry {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return ErrInvalid
		}
		body, err := io.ReadAll(io.LimitReader(r, MaxMetadata+1))
		_ = r.Close()
		if err != nil || len(body) > MaxMetadata {
			return ErrInvalid
		}
		return validateXML(entry, body)
	}
	return ErrInvalid
}

// ExtractArchive serves Artifactory's `site.zip!/` P2 URL form. The complete
// source archive is checked before its named update-site file is delivered.
func ExtractArchive(site []byte, entry string) ([]byte, error) {
	if len(site) == 0 || len(site) > MaxArtifact || Classify(entry) == Invalid {
		return nil, ErrInvalid
	}
	z, err := zip.NewReader(bytes.NewReader(site), int64(len(site)))
	if err != nil || len(z.File) == 0 || len(z.File) > 4096 {
		return nil, ErrInvalid
	}
	var selected *zip.File
	for _, f := range z.File {
		if f.Name == entry {
			selected = f
		}
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") ||
			strings.Contains(f.Name, "../") || f.UncompressedSize64 > MaxArtifact {
			return nil, ErrInvalid
		}
	}
	if selected == nil || selected.FileInfo().IsDir() {
		return nil, ErrInvalid
	}
	r, err := selected.Open()
	if err != nil {
		return nil, ErrInvalid
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(io.LimitReader(r, MaxArtifact+1))
	if err != nil || uint64(len(body)) != selected.UncompressedSize64 || r.Close() != nil {
		return nil, ErrInvalid
	}
	if err := Validate(entry, bytes.NewReader(body), int64(len(body))); err != nil {
		return nil, err
	}
	return body, nil
}

func validateIndex(r io.Reader, size int64) error {
	if size > 64<<10 {
		return ErrInvalid
	}
	s := bufio.NewScanner(io.LimitReader(r, size+1))
	s.Buffer(make([]byte, 4096), 64<<10)
	var version, metadata, artifacts bool
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ErrInvalid
		}
		switch strings.TrimSpace(key) {
		case "version":
			version = strings.TrimSpace(value) == "1"
		case "metadata.repository.factory.order", "artifact.repository.factory.order":
			if strings.TrimSpace(key) == "metadata.repository.factory.order" {
				metadata = true
			} else {
				artifacts = true
			}
			for _, file := range strings.Split(value, ",") {
				file = strings.TrimSpace(file)
				if file == `\!` {
					file = "!"
				}
				if file != "!" && Classify(file) != Metadata {
					return ErrInvalid
				}
			}
		default:
			return ErrInvalid
		}
	}
	if s.Err() != nil || !version || !metadata || !artifacts {
		return ErrInvalid
	}
	return nil
}

func validateXML(name string, body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var root bool
	var count int
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalid
		}
		count++
		if count > 500000 {
			return ErrInvalid
		}
		switch el := tok.(type) {
		case xml.Directive:
			return ErrInvalid
		case xml.StartElement:
			if !root {
				if el.Name.Local != "repository" {
					return ErrInvalid
				}
				var repositoryType string
				for _, attr := range el.Attr {
					if attr.Name.Local == "type" {
						repositoryType = attr.Value
					}
				}
				family := ".metadata.repository."
				if strings.HasPrefix(name, "artifacts") ||
					strings.HasPrefix(name, "compositeArtifacts") {
					family = ".artifact.repository."
				}
				if !strings.Contains(repositoryType, family) {
					return ErrInvalid
				}
				if strings.HasPrefix(name, "composite") &&
					!strings.Contains(repositoryType, "Composite") {
					return ErrInvalid
				}
				root = true
			}
			if el.Name.Local == "child" {
				var location string
				for _, attr := range el.Attr {
					if attr.Name.Local == "location" {
						location = attr.Value
					}
				}
				if Classify(location+"/content.xml") == Invalid ||
					strings.Contains(location, "..") || strings.Contains(location, ":") {
					return ErrInvalid
				}
			}
		}
	}
	if !root || !strings.HasSuffix(name, ".xml") {
		return ErrInvalid
	}
	return nil
}

type seekReaderAt struct{ io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek p2 JAR: %w", err)
	}
	n, err := io.ReadFull(r, p)
	if err != nil {
		return n, fmt.Errorf("read p2 JAR: %w", err)
	}
	return n, nil
}

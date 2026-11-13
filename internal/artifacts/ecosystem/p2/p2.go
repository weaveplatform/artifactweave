package p2

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// MaxMetadata, MaxArtifact, MaxChildren, MaxDepth and MaxChildURL bound
// what an update site can make the server read: 36 MiB for a metadata file
// and for its expanded content, 512 MiB for an artifact, a site archive and
// for their expanded entries, 64 children per composite metadata file, five
// composite hops and 2,048 bytes per child location. docs/testing/p2.md
// publishes these limits, so it changes with them.
const (
	MaxMetadata = 36 << 20
	MaxArtifact = 512 << 20
	MaxChildren = 64
	MaxDepth    = 5
	MaxChildURL = 2048
)

// ErrInvalid is the one error the p2 checks return for what they refuse:
// an invalid path, p2.index, metadata file, artifact, site archive or
// child location. It carries no detail of the content.
var ErrInvalid = errors.New("p2: invalid p2 repository content")

// Kind classifies the small set of native p2 files. An update site may place
// these under an archive suffix (site.zip!/content.jar) or a site directory.
type Kind uint8

// KindInvalid, the zero Kind, is any path the p2 protocol does not serve.
// KindIndex is p2.index, which names the metadata files a client reads;
// KindMetadata is a content or artifacts metadata file, plain or
// composite, as .xml, .jar or .xml.xz; and KindArtifact is a plugin or
// feature JAR or a binary artifact.
const (
	KindInvalid Kind = iota
	KindIndex
	KindMetadata
	KindArtifact
)

// Classify returns the Kind of an update-site path relative to the site:
// KindIndex for p2.index and KindMetadata for a metadata file, by base
// name, and KindArtifact for a .jar in a plugins or features folder or any
// file in a binary folder, at any depth. Any other file, and a path that
// is empty or absolute, has an empty, "." or ".." segment, or contains a
// backslash, NUL, CR, LF, "?" or "#", is KindInvalid.
func Classify(name string) Kind {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00\r\n?#") {
		return KindInvalid
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return KindInvalid
		}
	}
	base := path.Base(name)
	switch base {
	case "p2.index":
		return KindIndex
	case "content.xml", "content.jar", "compositeContent.xml", "compositeContent.jar",
		"artifacts.xml", "artifacts.jar", "compositeArtifacts.xml", "compositeArtifacts.jar",
		"content.xml.xz", "artifacts.xml.xz", "compositeContent.xml.xz", "compositeArtifacts.xml.xz":
		return KindMetadata
	}
	if strings.HasPrefix(name, "plugins/") || strings.HasPrefix(name, "features/") ||
		strings.HasPrefix(name, "binary/") || strings.Contains(name, "/plugins/") ||
		strings.Contains(name, "/features/") || strings.Contains(name, "/binary/") {
		if strings.HasPrefix(name, "binary/") || strings.Contains(name, "/binary/") {
			return KindArtifact
		}
		if strings.HasSuffix(base, ".jar") {
			return KindArtifact
		}
	}
	return KindInvalid
}

// Validate reads the complete representation before the source cache may
// publish it. zip readers check both directory structure and entry CRCs.
// ctx ends a wait for the xz dictionary budget, which is then a refusal
// too.
func Validate(ctx context.Context, name string, src io.ReadSeeker, size int64) error {
	kind := Classify(name)
	limit := int64(MaxMetadata)
	if kind == KindArtifact {
		limit = MaxArtifact
	}
	if kind == KindInvalid || size <= 0 || size > limit {
		return ErrInvalid
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return ErrInvalid
	}
	if kind == KindIndex {
		return validateIndex(src, size)
	}
	if strings.HasSuffix(name, ".xml.xz") {
		// The p2 client's XZInputStream refuses a file whose index and
		// footer are missing, so the whole file is read (#1331).
		r, err := xzfile.NewReader(ctx, src, MaxMetadata+1)
		if err != nil {
			return ErrInvalid
		}
		defer func() { _ = r.Close() }()
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
	maxExpanded := uint64(MaxArtifact)
	if kind == KindMetadata {
		maxExpanded = MaxMetadata
	}
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxExpanded || total > maxExpanded-f.UncompressedSize64 {
			return ErrInvalid
		}
		total += f.UncompressedSize64
		r, err := f.Open()
		if err != nil {
			return ErrInvalid
		}
		_, err = io.Copy(io.Discard, io.LimitReader(r, int64(maxExpanded)+1))
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			return ErrInvalid
		}
	}
	if kind != KindMetadata {
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
func ExtractArchive(ctx context.Context, site []byte, entry string) ([]byte, error) {
	if len(site) == 0 || len(site) > MaxArtifact || Classify(entry) == KindInvalid {
		return nil, ErrInvalid
	}
	z, err := zip.NewReader(bytes.NewReader(site), int64(len(site)))
	if err != nil || len(z.File) == 0 || len(z.File) > 4096 {
		return nil, ErrInvalid
	}
	var selected *zip.File
	var expanded uint64
	for _, f := range z.File {
		if f.Name == entry {
			selected = f
		}
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") ||
			strings.Contains(f.Name, "../") || f.UncompressedSize64 > MaxArtifact {
			return nil, ErrInvalid
		}
		expanded += f.UncompressedSize64
		if expanded > MaxArtifact {
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
	if err := Validate(ctx, entry, bytes.NewReader(body), int64(len(body))); err != nil {
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
				if file != "!" && Classify(file) != KindMetadata {
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
	var count, children int
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
				children++
				if children > MaxChildren || !ValidChildLocation(location) {
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

// ValidChildLocation accepts repository-relative locations and absolute HTTP(S)
// locations. The server separately checks configured origin policy before any
// child is exposed. This syntax check also rejects encoded traversal.
func ValidChildLocation(raw string) bool {
	if raw == "" || len(raw) > MaxChildURL || strings.ContainsAny(raw, "\\\x00\r\n#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		(u.IsAbs() && (u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "")) ||
		(!u.IsAbs() && (u.Host != "" || strings.HasPrefix(raw, "/"))) {
		return false
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil || decoded == "" || strings.ContainsAny(decoded, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

type seekReaderAt struct{ io.ReadSeeker }

func (r seekReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek p2 jar: %w", err)
	}
	n, err := io.ReadFull(r, p)
	if err != nil {
		return n, fmt.Errorf("read p2 jar: %w", err)
	}
	return n, nil
}

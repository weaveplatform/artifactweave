package maven

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// ErrRemoteContent identifies invalid source bytes without echoing upstream XML.
var ErrRemoteContent = errors.New("invalid Maven source content")

// SourceMetadata is the authoritative version source consumed by native Maven
// resolution and P9 upstream version search. It never reads the local package
// index and therefore includes versions with no cached binaries.
type SourceMetadata struct {
	XMLName    xml.Name         `xml:"metadata"`
	GroupID    string           `xml:"groupId"`
	ArtifactID string           `xml:"artifactId"`
	Version    string           `xml:"version"`
	Versioning SourceVersioning `xml:"versioning"`
}

type SourceVersioning struct {
	Latest    string                  `xml:"latest"`
	Release   string                  `xml:"release"`
	Versions  []string                `xml:"versions>version"`
	Snapshot  SourceSnapshot          `xml:"snapshot"`
	Snapshots []SourceSnapshotVersion `xml:"snapshotVersions>snapshotVersion"`
}

type SourceSnapshot struct {
	Timestamp   string `xml:"timestamp"`
	BuildNumber int    `xml:"buildNumber"`
}

type SourceSnapshotVersion struct {
	Classifier string `xml:"classifier"`
	Extension  string `xml:"extension"`
	Value      string `xml:"value"`
}

// ReadSourceMetadata bounds XML before parsing it; the original representation
// is retained separately and served without rewriting to the cached subset.
func ReadSourceMetadata(r io.Reader) (SourceMetadata, error) {
	var out SourceMetadata
	b, err := io.ReadAll(io.LimitReader(r, maxPOM+1))
	if err != nil {
		return out, fmt.Errorf("read Maven source metadata: %w", err)
	}
	if len(b) > maxPOM {
		return out, ErrRemoteContent
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	d.CharsetReader = pomCharset
	if err = d.Decode(&out); err != nil {
		return SourceMetadata{}, ErrRemoteContent
	}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return SourceMetadata{}, ErrRemoteContent
		}
		switch value := tok.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return SourceMetadata{}, ErrRemoteContent
			}
		case xml.Comment, xml.ProcInst:
		default:
			return SourceMetadata{}, ErrRemoteContent
		}
	}
}

// ValidateRemote checks complete source bytes before they can enter the cache or
// a no-store response. POM checks apply to the canonical repository layout.
func ValidateRemote(
	ctx context.Context,
	c repo.Config,
	l *layout.Layout,
	path string,
	r io.ReadSeeker,
	size int64,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validate Maven source: %w", err)
	}
	if IsMetadata(path) {
		_, err := ReadSourceMetadata(r)
		return err
	}
	if IsPom(path) && !suppressed(c) {
		b, err := io.ReadAll(io.LimitReader(r, maxPOM+1))
		if err != nil {
			return fmt.Errorf("read source POM: %w", err)
		}
		if p := parsePOM(b); p.problem(path, reader{l}.integration(path)) != "" {
			return ErrRemoteContent
		}
	}
	if strings.HasSuffix(path, ".jar") && c.RejectInvalidJars != nil && *c.RejectInvalidJars {
		return validateJar(ctx, r, size)
	}
	return nil
}

type seekAt struct{ io.ReadSeeker }

func (r seekAt) ReadAt(b []byte, off int64) (int, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek JAR: %w", err)
	}
	n, err := io.ReadFull(r, b)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err //nolint:wrapcheck // ReaderAt requires the EOF sentinel
}

func validateJar(ctx context.Context, r io.ReadSeeker, size int64) error {
	z, err := zip.NewReader(seekAt{r}, size)
	if err != nil || len(z.File) > 10000 {
		return ErrRemoteContent
	}
	remaining := int64(512 << 20)
	for _, f := range z.File {
		if err = ctx.Err(); err != nil {
			return fmt.Errorf("validate source JAR: %w", err)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		body, openErr := f.Open()
		if openErr != nil {
			return ErrRemoteContent
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(body, remaining+1))
		_ = body.Close()
		if readErr != nil || n > remaining {
			return ErrRemoteContent
		}
		remaining -= n
	}
	return nil
}

package p2

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// ArtifactChecksums reads the checksums declared for a standard P2 artifact
// path. An absent declaration leaves the structural JAR validation in force.
// Invalid declarations fail closed instead of silently disabling pinning.
func ArtifactChecksums(
	ctx context.Context,
	metadataName string,
	body []byte,
	artifactPath string,
) (kernel.Checksums, error) {
	if !strings.HasPrefix(path.Base(metadataName), "artifacts.") ||
		Validate(ctx, metadataName, bytes.NewReader(body), int64(len(body))) != nil {
		return kernel.Checksums{}, ErrInvalid
	}
	xmlBody, err := metadataXML(ctx, metadataName, body)
	if err != nil {
		return kernel.Checksums{}, err
	}
	dec := xml.NewDecoder(bytes.NewReader(xmlBody))
	var found, matching bool
	var checksums kernel.Checksums
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return kernel.Checksums{}, ErrInvalid
		}
		switch el := token.(type) {
		case xml.StartElement:
			if el.Name.Local == "artifact" {
				var classifier, id, version string
				for _, a := range el.Attr {
					switch a.Name.Local {
					case "classifier":
						classifier = a.Value
					case "id":
						id = a.Value
					case "version":
						version = a.Value
					}
				}
				candidate := ""
				switch classifier {
				case "osgi.bundle":
					candidate = "plugins/" + id + "_" + version + ".jar"
				case "org.eclipse.update.feature":
					candidate = "features/" + id + "_" + version + ".jar"
				case "binary":
					candidate = "binary/" + id + "_" + version
				}
				matching = candidate == artifactPath
				if matching {
					if found {
						return kernel.Checksums{}, ErrInvalid
					}
					found = true
				}
			} else if el.Name.Local == "property" && matching {
				var name, value string
				for _, a := range el.Attr {
					if a.Name.Local == "name" {
						name = a.Value
					}
					if a.Name.Local == "value" {
						value = a.Value
					}
				}
				switch name {
				case "download.checksum.sha-256":
					checksums.SHA256 = value
				case "download.checksum.sha-512":
					checksums.SHA512 = value
				}
			}
		case xml.EndElement:
			if el.Name.Local == "artifact" {
				matching = false
			}
		}
	}
	if !found {
		return kernel.Checksums{}, ErrInvalid
	}
	if !validDigest(checksums.SHA256, 64) || !validDigest(checksums.SHA512, 128) {
		return kernel.Checksums{}, ErrInvalid
	}
	return checksums, nil
}

func validDigest(raw string, length int) bool {
	if raw == "" {
		return true
	}
	if len(raw) != length {
		return false
	}
	for _, c := range raw {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func metadataXML(ctx context.Context, name string, body []byte) ([]byte, error) {
	switch {
	case strings.HasSuffix(name, ".xml"):
		return body, nil
	case strings.HasSuffix(name, ".xml.xz"):
		r, err := xzfile.NewReader(ctx, bytes.NewReader(body), MaxMetadata+1)
		if err != nil {
			return nil, ErrInvalid
		}
		defer func() { _ = r.Close() }()
		out, err := io.ReadAll(io.LimitReader(r, MaxMetadata+1))
		if err != nil || len(out) > MaxMetadata {
			return nil, ErrInvalid
		}
		return out, nil
	case strings.HasSuffix(name, ".jar"):
		z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, ErrInvalid
		}
		entry := strings.TrimSuffix(path.Base(name), ".jar") + ".xml"
		for _, f := range z.File {
			if f.Name != entry {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return nil, ErrInvalid
			}
			out, err := io.ReadAll(io.LimitReader(r, MaxMetadata+1))
			_ = r.Close()
			if err != nil || len(out) > MaxMetadata {
				return nil, ErrInvalid
			}
			return out, nil
		}
	}
	return nil, ErrInvalid
}

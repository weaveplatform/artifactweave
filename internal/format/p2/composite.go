package p2

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/ulikunitz/xz"
)

// EditComposite reads any supported composite metadata representation, calls
// replace for each child location, and returns its complete rewritten bytes.
// No original location reaches a client if one replacement fails.
//
//nolint:gocyclo // The format's XML, JAR, and XZ encodings share one fail-closed rewrite path.
func EditComposite(name string, body []byte, replace func(string) (string, error)) ([]byte, error) {
	if !strings.HasPrefix(path.Base(name), "composite") ||
		Validate(name, bytes.NewReader(body), int64(len(body))) != nil {
		return nil, ErrInvalid
	}
	entry := path.Base(name)
	var xmlBody []byte
	switch {
	case strings.HasSuffix(name, ".xml"):
		xmlBody = body
	case strings.HasSuffix(name, ".xml.xz"):
		r, err := xz.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, ErrInvalid
		}
		xmlBody, err = io.ReadAll(io.LimitReader(r, MaxMetadata+1))
		if err != nil || len(xmlBody) > MaxMetadata {
			return nil, ErrInvalid
		}
		entry = strings.TrimSuffix(entry, ".xz")
	case strings.HasSuffix(name, ".jar"):
		entry = strings.TrimSuffix(entry, ".jar") + ".xml"
		z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, ErrInvalid
		}
		for _, f := range z.File {
			if f.Name != entry {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return nil, ErrInvalid
			}
			xmlBody, err = io.ReadAll(io.LimitReader(r, MaxMetadata+1))
			_ = r.Close()
			if err != nil || len(xmlBody) > MaxMetadata {
				return nil, ErrInvalid
			}
			break
		}
	default:
		return nil, ErrInvalid
	}
	if len(xmlBody) == 0 {
		return nil, ErrInvalid
	}
	var rewritten bytes.Buffer
	dec := xml.NewDecoder(bytes.NewReader(xmlBody))
	enc := xml.NewEncoder(&rewritten)
	children := 0
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrInvalid
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "child" {
			children++
			if children > MaxChildren {
				return nil, ErrInvalid
			}
			locations := 0
			for i := range start.Attr {
				if start.Attr[i].Name.Local != "location" {
					continue
				}
				locations++
				if !ValidChildLocation(start.Attr[i].Value) {
					return nil, ErrInvalid
				}
				value, err := replace(start.Attr[i].Value)
				if err != nil {
					return nil, err
				}
				start.Attr[i].Value = value
			}
			if locations != 1 {
				return nil, ErrInvalid
			}
			token = start
		}
		if _, ok := token.(xml.Directive); ok || enc.EncodeToken(token) != nil ||
			rewritten.Len() > MaxMetadata {
			return nil, ErrInvalid
		}
	}
	if enc.Flush() != nil || rewritten.Len() > MaxMetadata {
		return nil, ErrInvalid
	}
	if strings.HasSuffix(name, ".xml") {
		return rewritten.Bytes(), nil
	}
	var packed bytes.Buffer
	if strings.HasSuffix(name, ".xz") {
		w, err := xz.NewWriter(&packed)
		if err != nil {
			return nil, ErrInvalid
		}
		_, err = w.Write(rewritten.Bytes())
		if err != nil || w.Close() != nil || packed.Len() > MaxMetadata {
			return nil, ErrInvalid
		}
		return packed.Bytes(), nil
	}
	original, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, ErrInvalid
	}
	writer := zip.NewWriter(&packed)
	for _, f := range original.File {
		h := f.FileHeader
		w, err := writer.CreateHeader(&h)
		if err != nil {
			return nil, ErrInvalid
		}
		if f.Name == entry {
			_, err = w.Write(rewritten.Bytes())
		} else {
			var r io.ReadCloser
			r, err = f.Open()
			if err == nil {
				_, err = io.Copy(w, io.LimitReader(r, MaxMetadata+1))
				_ = r.Close()
			}
		}
		if err != nil || packed.Len() > MaxMetadata {
			return nil, ErrInvalid
		}
	}
	if writer.Close() != nil || packed.Len() > MaxMetadata ||
		Validate(name, bytes.NewReader(packed.Bytes()), int64(packed.Len())) != nil {
		return nil, ErrInvalid
	}
	return packed.Bytes(), nil
}

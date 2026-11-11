package helm_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// file is an archive entry: a folder when its name ends in "/", and a
// symbolic link when link is set.
type file struct {
	name, body string
	link       bool
}

// tarOf is files as a tar, gzipped when gz is set. Writes to a buffer
// cannot fail, so neither can this.
func tarOf(gz bool, files ...file) []byte {
	var buf bytes.Buffer
	var w io.Writer = &buf
	var g *gzip.Writer
	if gz {
		g = gzip.NewWriter(&buf)
		w = g
	}
	tw := tar.NewWriter(w)
	for _, f := range files {
		h := &tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(f.name, "/"):
			h.Typeflag, h.Size = tar.TypeDir, 0
		case f.link:
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, "elsewhere"
		}
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = io.WriteString(tw, f.body)
		}
	}
	_ = tw.Close()
	if g != nil {
		_ = g.Close()
	}
	return buf.Bytes()
}

func tgz(files ...file) []byte { return tarOf(true, files...) }

// chartTgz is a chart archive as helm package writes one: the chart's
// folder, named after it, holding Chart.yaml with name, version and the
// extra lines, and a template.
func chartTgz(name, version string, extra ...string) []byte {
	return tgz(
		file{name: name + "/Chart.yaml", body: chartYAML(name, version, extra...)},
		file{name: name + "/values.yaml", body: "replicas: 1\n"},
		file{name: name + "/templates/deployment.yaml", body: "kind: Deployment\n"},
	)
}

// chartYAML is a v2 Chart.yaml naming name and version, with the extra
// lines after them.
func chartYAML(name, version string, extra ...string) string {
	return fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version) +
		strings.Join(extra, "\n")
}

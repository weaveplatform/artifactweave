package cran

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

const (
	maxIndexBytes   = 32 << 20
	maxIndexEntries = 100000
)

var indexFields = []string{
	"Depends", "Imports", "LinkingTo", "Suggests", "Enhances", "License",
	"NeedsCompilation", "OS_type", "Archs", "Priority", "Built", "MD5sum",
}

// EncodeIndex records only the newest version per package, as a CRAN client
// expects from a terminal PACKAGES directory. Older files remain addressable.
func EncodeIndex(in []Package, feed string) (map[string][]byte, error) {
	if _, ok := Feed(feed); !ok || len(in) > maxIndexEntries {
		return nil, ErrPackage
	}
	latest := map[string]Package{}
	for _, pkg := range in {
		if pkg.Feed != feed || !ValidName(pkg.Name) || !ValidVersion(pkg.Version) ||
			!packageMatches(pkg) {
			return nil, ErrPackage
		}
		old, exists := latest[pkg.Name]
		if !exists || CompareVersion(pkg.Version, old.Version) > 0 {
			latest[pkg.Name] = pkg
		}
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	slices.Sort(names)
	var out strings.Builder
	for _, name := range names {
		p := latest[name]
		if out.Len() > maxIndexBytes {
			return nil, ErrPackage
		}
		out.WriteString("Package: " + p.Name + "\nVersion: " + p.Version + "\n")
		out.WriteString("File: " + path.Base(p.Path) + "\n")
		for _, field := range indexFields {
			value := p.Fields[field]
			if field == "MD5sum" && p.MD5 != "" {
				value = p.MD5
			}
			if value != "" {
				if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
					return nil, ErrPackage
				}
				out.WriteString(field + ": " + value + "\n")
			}
		}
		out.WriteByte('\n')
	}
	if out.Len() > maxIndexBytes {
		return nil, ErrPackage
	}
	plain := []byte(out.String())
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(plain); err != nil {
		return nil, fmt.Errorf("cran: write compressed index: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("cran: close compressed index: %w", err)
	}
	return map[string][]byte{PackagesName: plain, PackagesGzipName: compressed.Bytes()}, nil
}

func DecodeIndex(body []byte, indexPath string) ([]Package, error) {
	feed, name, ok := IndexPath(indexPath)
	if !ok || len(body) > maxIndexBytes {
		return nil, ErrPackage
	}
	if name == PackagesGzipName {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, ErrPackage
		}
		plain, err := io.ReadAll(io.LimitReader(gz, maxIndexBytes+1))
		closeErr := gz.Close()
		if err != nil || closeErr != nil || len(plain) > maxIndexBytes {
			return nil, ErrPackage
		}
		body = plain
	}
	if len(body) == 0 {
		return []Package{}, nil
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	blocks := strings.Split(strings.TrimSpace(text), "\n\n")
	if len(blocks) > maxIndexEntries {
		return nil, ErrPackage
	}
	out := make([]Package, 0, len(blocks))
	seen := map[string]bool{}
	for _, block := range blocks {
		pkg, err := decodeIndexEntry(block, feed)
		if err != nil || seen[pkg.Name] {
			return nil, ErrPackage
		}
		seen[pkg.Name] = true
		out = append(out, pkg)
	}
	return out, nil
}

func decodeIndexEntry(block, feed string) (Package, error) {
	fields, err := parseFields([]byte(block))
	if err != nil {
		return Package{}, err
	}
	file := fields["File"]
	if file == "" {
		suffix := ".tar.gz"
		if strings.HasPrefix(feed, "bin/windows/") {
			suffix = ".zip"
		} else if strings.HasPrefix(feed, "bin/macosx/") {
			suffix = ".tgz"
		}
		file = fields["Package"] + "_" + fields["Version"] + suffix
	}
	if path.Base(file) != file || file == "." || file == ".." {
		return Package{}, ErrPackage
	}
	folder := feed
	if sub := fields["Path"]; sub != "" {
		if path.Clean(sub) != sub || strings.HasPrefix(sub, "/") || strings.Contains(sub, "\\") {
			return Package{}, ErrPackage
		}
		for _, segment := range strings.Split(sub, "/") {
			if segment == "." || segment == ".." || !buildName.MatchString(segment) {
				return Package{}, ErrPackage
			}
		}
		folder += "/" + sub
	}
	p := folder + "/" + file
	pfeed, pname, pversion, ok := PackagePath(p)
	if !ok || pfeed != feed || pname != fields["Package"] || pversion != fields["Version"] {
		return Package{}, ErrPackage
	}
	pkg := Package{Name: pname, Version: pversion, Path: p, Feed: feed, Fields: fields}
	if md5 := fields["MD5sum"]; md5 != "" {
		if len(md5) != 32 {
			return Package{}, ErrPackage
		}
		if _, err := hex.DecodeString(md5); err != nil {
			return Package{}, ErrPackage
		}
		pkg.MD5 = strings.ToLower(md5)
	}
	return pkg, nil
}

func packageMatches(pkg Package) bool {
	feed, name, version, ok := PackagePath(pkg.Path)
	return ok && feed == pkg.Feed && name == pkg.Name && version == pkg.Version
}

// CompareVersion follows R's numeric component ordering for version labels;
// separators divide components and numeric chunks compare by value.
func CompareVersion(a, b string) int {
	as, bs := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' }),
		strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' })
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		an, ae := strconv.ParseUint(as[i], 10, 64)
		bn, be := strconv.ParseUint(bs[i], 10, 64)
		var n int
		if ae == nil && be == nil {
			if an < bn {
				n = -1
			} else if an > bn {
				n = 1
			}
		} else {
			n = strings.Compare(as[i], bs[i])
		}
		if n != 0 {
			return n
		}
	}
	return 0
}

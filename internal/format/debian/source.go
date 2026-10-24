package debian

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Debian Sources wire checksum.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

const maxSourceControl = 1 << 20

// SourceIndexRecord lists the artifact paths advertised by one validated
// Sources stanza. It lets the HTTP layer enforce read permissions before
// exposing a source index to an apt client.
type SourceIndexRecord struct {
	Files []string
}

// DecodeSourcesIndex bounds and validates plain or gzip apt source indexes.
func DecodeSourcesIndex(body []byte) ([]SourceIndexRecord, error) {
	var err error
	body, err = decodeIndexCompression(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidSource, err)
	}
	if len(body) > maxPackagesIndex || bytes.IndexByte(body, 0) >= 0 {
		return nil, fmt.Errorf("%w: Sources size or contents", errInvalidSource)
	}
	if len(body) == 0 {
		return nil, nil
	}
	if !bytes.HasSuffix(body, []byte("\n\n")) {
		return nil, fmt.Errorf("%w: incomplete Sources stanza", errInvalidSource)
	}
	stanzas := bytes.Split(body[:len(body)-2], []byte("\n\n"))
	if len(stanzas) > maxPackageRows {
		return nil, fmt.Errorf("%w: too many Sources stanzas", errInvalidSource)
	}
	out := make([]SourceIndexRecord, 0, len(stanzas))
	for _, stanza := range stanzas {
		fields, err := sourceFields(stanza)
		if err != nil || !packageName.MatchString(fields["package"]) ||
			fields["version"] == "" ||
			strings.ContainsAny(fields["version"], " \t\r\n/") ||
			!validIndexPath(fields["directory"]) {
			return nil, fmt.Errorf("%w: invalid Sources stanza", errInvalidSource)
		}
		md5s, err := sourceChecksums(fields["files"], md5.Size)
		if err != nil {
			return nil, err
		}
		sha256s, err := sourceChecksums(fields["checksums-sha256"], sha256.Size)
		if err != nil || len(md5s) != len(sha256s) {
			return nil, fmt.Errorf("%w: Sources checksums", errInvalidSource)
		}
		record := SourceIndexRecord{Files: make([]string, 0, len(md5s))}
		descriptor := false
		for name, m := range md5s {
			sha, ok := sha256s[name]
			if !ok || m.Size != sha.Size {
				return nil, fmt.Errorf("%w: Sources checksum mismatch", errInvalidSource)
			}
			p := path.Join(fields["directory"], name)
			if !validIndexPath(p) {
				return nil, fmt.Errorf("%w: Sources file path", errInvalidSource)
			}
			descriptor = descriptor || strings.HasSuffix(name, ".dsc")
			record.Files = append(record.Files, p)
		}
		if !descriptor {
			return nil, fmt.Errorf("%w: Sources descriptor missing", errInvalidSource)
		}
		slices.Sort(record.Files)
		out = append(out, record)
	}
	return out, nil
}

var (
	errInvalidSource = errors.New("debian: invalid source package")
	controlKey       = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

// SourcePart is one archive named by a .dsc file. Its declarations are not
// trusted until the repository has compared them to stored server checksums.
type SourcePart struct {
	Name, MD5, SHA256 string
	Size              int64
}

// Source is the parsed source control file and its declared archive parts.
type Source struct {
	Name, Version, Format string
	Control               []byte
	Parts                 []SourcePart
}

// ReadSource parses an unsigned or OpenPGP clear-signed .dsc without accepting
// a second stanza or unbounded input. The repository verifies archive digests
// separately before any Sources entry is published.
func ReadSource(ctx context.Context, r io.Reader, size int64) (Source, error) {
	if size <= 0 || size > maxSourceControl {
		return Source{}, fmt.Errorf("%w: .dsc size", errInvalidSource)
	}
	if err := ctx.Err(); err != nil {
		return Source{}, fmt.Errorf("debian: source parse canceled: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(r, maxSourceControl+1))
	if err != nil || int64(len(body)) != size {
		return Source{}, fmt.Errorf("%w: truncated .dsc", errInvalidSource)
	}
	control := body
	if bytes.HasPrefix(body, []byte("-----BEGIN PGP SIGNED MESSAGE-----")) {
		block, rest := clearsign.Decode(body)
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			return Source{}, fmt.Errorf("%w: malformed signed .dsc", errInvalidSource)
		}
		control = block.Plaintext
	}
	fields, err := sourceFields(control)
	if err != nil {
		return Source{}, err
	}
	name, version := fields["source"], fields["version"]
	if !packageName.MatchString(name) || version == "" ||
		strings.ContainsAny(version, " \t\r\n/") ||
		fields["format"] == "" || fields["maintainer"] == "" ||
		fields["standards-version"] == "" {
		return Source{}, fmt.Errorf("%w: mandatory .dsc fields", errInvalidSource)
	}
	md5, err := sourceChecksums(fields["files"], 16)
	if err != nil {
		return Source{}, err
	}
	sha256, err := sourceChecksums(fields["checksums-sha256"], 32)
	if err != nil {
		return Source{}, err
	}
	if len(md5) != len(sha256) || len(md5) == 0 {
		return Source{}, fmt.Errorf("%w: incomplete source checksums", errInvalidSource)
	}
	parts := make([]SourcePart, 0, len(sha256))
	for name, sha := range sha256 {
		m, ok := md5[name]
		if !ok || m.Size != sha.Size {
			return Source{}, fmt.Errorf(
				"%w: source checksum mismatch for %s",
				errInvalidSource,
				name,
			)
		}
		parts = append(parts, SourcePart{
			Name: name, Size: sha.Size, MD5: m.MD5,
			SHA256: sha.SHA256,
		})
	}
	slices.SortFunc(parts, func(a, b SourcePart) int { return strings.Compare(a.Name, b.Name) })
	return Source{
		Name: name, Version: version, Format: fields["format"],
		Control: control, Parts: parts,
	}, nil
}

func sourceFields(control []byte) (map[string]string, error) {
	return controlFields(control, maxSourceControl)
}

func controlFields(control []byte, limit int) (map[string]string, error) {
	if len(control) == 0 || len(control) > limit || bytes.IndexByte(control, 0) >= 0 {
		return nil, fmt.Errorf("%w: control contents", errInvalidSource)
	}
	parts := map[string][]string{}
	var current string
	for _, line := range strings.Split(strings.TrimRight(string(control), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return nil, fmt.Errorf("%w: extra source stanza", errInvalidSource)
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current == "" {
				return nil, fmt.Errorf("%w: orphan source continuation", errInvalidSource)
			}
			parts[current] = append(parts[current], line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.ToLower(key)
		_, duplicate := parts[key]
		if !ok || !controlKey.MatchString(key) || duplicate {
			return nil, fmt.Errorf("%w: malformed source field", errInvalidSource)
		}
		parts[key] = []string{strings.TrimLeft(value, " \t")}
		current = key
	}
	fields := make(map[string]string, len(parts))
	for key, lines := range parts {
		fields[key] = strings.Join(lines, "\n")
	}
	return fields, nil
}

func sourceChecksums(value string, digestBytes int) (map[string]SourcePart, error) {
	lines := strings.Split(value, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "" || len(lines) > 257 {
		return nil, fmt.Errorf("%w: source checksum list", errInvalidSource)
	}
	parts := map[string]SourcePart{}
	for _, line := range lines[1:] {
		if len(line) == 0 || line[0] != ' ' {
			return nil, fmt.Errorf("%w: source checksum continuation", errInvalidSource)
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || len(fields[0]) != digestBytes*2 ||
			!validSourceName(fields[2]) {
			return nil, fmt.Errorf("%w: source checksum row", errInvalidSource)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("%w: source checksum hex", errInvalidSource)
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size <= 0 || size > maxBinarySize || parts[fields[2]].Name != "" {
			return nil, fmt.Errorf("%w: source checksum size or duplicate", errInvalidSource)
		}
		p := SourcePart{Name: fields[2], Size: size}
		if digestBytes == 16 {
			p.MD5 = strings.ToLower(fields[0])
		} else {
			p.SHA256 = strings.ToLower(fields[0])
		}
		parts[p.Name] = p
	}
	return parts, nil
}

func validSourceName(name string) bool {
	return name != "" && name == path.Base(name) && name != "." && name != ".." &&
		!strings.Contains(name, "\\") && strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) < 0
}

// IndexedSource binds a parsed .dsc to the stored bytes for it and each named
// archive. Source records are emitted only after all declared bytes match.
type IndexedSource struct {
	Source    Source
	Path      string
	Size      int64
	Checksums types.Checksums
	Archives  []SourceArchive
}

type SourceArchive struct {
	Name      string
	Size      int64
	Checksums types.Checksums
}

// Sources emits the deb-src index with server-computed checksums. Client
// supplied Directory, Files and Checksums fields never reach the output.
func Sources(entries []IndexedSource) ([]byte, error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b IndexedSource) int {
		if n := strings.Compare(a.Source.Name, b.Source.Name); n != 0 {
			return n
		}
		if n := strings.Compare(a.Source.Version, b.Source.Version); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	})
	var out strings.Builder
	for _, entry := range entries {
		record, err := entry.record()
		if err != nil {
			return nil, err
		}
		out.WriteString(record)
	}
	return []byte(out.String()), nil
}

func (s IndexedSource) record() (string, error) {
	if !validIndexPath(s.Path) || !strings.HasSuffix(s.Path, ".dsc") ||
		s.Size <= 0 || len(s.Archives) != len(s.Source.Parts) {
		return "", fmt.Errorf("%w: source index path or archive count", errInvalidSource)
	}
	all := make([]SourceArchive, 0, len(s.Archives)+1)
	all = append(all, SourceArchive{
		Name: path.Base(s.Path), Size: s.Size,
		Checksums: s.Checksums,
	})
	all = append(all, s.Archives...)
	slices.SortFunc(all, func(a, b SourceArchive) int { return strings.Compare(a.Name, b.Name) })
	declared := map[string]SourcePart{}
	for _, part := range s.Source.Parts {
		declared[part.Name] = part
	}
	for _, file := range all {
		if !validSourceName(file.Name) || file.Size <= 0 ||
			len(file.Checksums.MD5) != md5.Size*2 ||
			len(file.Checksums.SHA256) != sha256.Size*2 {
			return "", fmt.Errorf("%w: source archive digest or name", errInvalidSource)
		}
		if file.Name == path.Base(s.Path) {
			continue
		}
		part, ok := declared[file.Name]
		if !ok || part.Size != file.Size ||
			!strings.EqualFold(part.MD5, file.Checksums.MD5) ||
			!strings.EqualFold(part.SHA256, file.Checksums.SHA256) {
			return "", fmt.Errorf("%w: source archive integrity", errInvalidSource)
		}
		delete(declared, file.Name)
	}
	if len(declared) != 0 {
		return "", fmt.Errorf("%w: source archive missing", errInvalidSource)
	}
	var out strings.Builder
	skip := false
	for _, line := range strings.Split(strings.TrimSuffix(string(s.Source.Control), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return "", fmt.Errorf("%w: extra source control stanza", errInvalidSource)
		}
		if line[0] != ' ' && line[0] != '\t' {
			key, _, _ := strings.Cut(line, ":")
			switch strings.ToLower(key) {
			case "source":
				out.WriteString("Package: ")
				out.WriteString(s.Source.Name)
				out.WriteByte('\n')
				skip = true
			case "package", "directory", "files", "checksums-sha1", "checksums-sha256":
				skip = true
			default:
				skip = false
			}
		}
		if !skip {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	out.WriteString("Directory: ")
	out.WriteString(path.Dir(s.Path))
	out.WriteString("\nFiles:\n")
	for _, file := range all {
		fmt.Fprintf(&out, " %s %d %s\n", file.Checksums.MD5, file.Size, file.Name)
	}
	out.WriteString("Checksums-Sha256:\n")
	for _, file := range all {
		fmt.Fprintf(&out, " %s %d %s\n", file.Checksums.SHA256, file.Size, file.Name)
	}
	out.WriteByte('\n')
	return out.String(), nil
}

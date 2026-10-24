package debian

import (
	"bytes"
	"context"
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
)

const maxSourceControl = 1 << 20

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
	if len(control) == 0 || len(control) > maxSourceControl || bytes.IndexByte(control, 0) >= 0 {
		return nil, fmt.Errorf("%w: control contents", errInvalidSource)
	}
	fields := map[string]string{}
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
			fields[current] += "\n" + line
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.ToLower(key)
		_, duplicate := fields[key]
		if !ok || !controlKey.MatchString(key) || duplicate {
			return nil, fmt.Errorf("%w: malformed source field", errInvalidSource)
		}
		fields[key] = strings.TrimLeft(value, " \t")
		current = key
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

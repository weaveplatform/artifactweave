package cocoapods

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // CocoaPods CDN uses MD5 only for sharding, not integrity.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxArchiveBytes  = 64 << 20
	maxExpandedBytes = 256 << 20
	maxSpecBytes     = 2 << 20
	maxArchiveFiles  = 10000
)

var podVersion = regexp.MustCompile(`^[0-9][A-Za-z0-9.+-]*$`)

type Archive struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Spec    json.RawMessage `json:"spec"`
	SHA256  string          `json:"sha256"`
}

// CocoaPods' public CDN includes names with punctuation, spaces and Unicode.
// The protocol treats a pod name as one path segment, so reject only separators
// and control characters rather than imposing a narrower invented grammar.
func ValidName(v string) bool {
	if v == "" || len(v) > 128 || !utf8.ValidString(v) || v != strings.TrimSpace(v) ||
		v == "." || v == ".." || strings.ContainsAny(v, `/\\?#`) {
		return false
	}
	for _, ch := range v {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

func ValidVersion(v string) bool { return len(v) <= 128 && podVersion.MatchString(v) }

func Path(name, version string) string {
	return name + "/" + version + "/" + name + "-" + version + ".tar.gz"
}

func ParsePath(p string) (name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidName(parts[0]) || !ValidVersion(parts[1]) ||
		p != Path(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func Shard(name string) string {
	sum := md5.Sum([]byte(name)) //nolint:gosec // CDN path selection only.
	h := hex.EncodeToString(sum[:])
	return h[0:1] + "/" + h[1:2] + "/" + h[2:3]
}

func ShardFile(name string) string {
	return "all_pods_versions_" + strings.ReplaceAll(Shard(name), "/", "_") + ".txt"
}

func SpecPath(name, version string) string {
	return "Specs/" + Shard(name) + "/" + name + "/" + version + "/" + name + ".podspec.json"
}

// ReadArchive accepts an actual CocoaPods tar.gz and one JSON podspec at its
// root. It reads every header and compressed trailer before the version may be
// indexed. This prevents a corrupt or path-escaping archive from becoming a
// resolvable package through an otherwise valid podspec.
//
//nolint:gocyclo // Each archive header, path and podspec invariant must fail closed.
func ReadArchive(body io.Reader, size int64, name, version string) (Archive, error) {
	if size <= 0 || size > MaxArchiveBytes || !ValidName(name) || !ValidVersion(version) {
		return Archive{}, invalid("invalid CocoaPods archive size or identity")
	}
	gz, err := gzip.NewReader(io.LimitReader(body, MaxArchiveBytes+1))
	if err != nil {
		return Archive{}, fmt.Errorf("gzip header: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var spec json.RawMessage
	var count int
	var expanded int64
	seen := make(map[string]bool)
	for {
		h, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil || h == nil || h.Size < 0 {
			return Archive{}, invalid("invalid tar header")
		}
		count++
		expanded += h.Size
		if count > maxArchiveFiles || expanded > maxExpandedBytes ||
			strings.HasPrefix(h.Name, "/") || strings.ContainsAny(h.Name, "\\\x00") {
			return Archive{}, invalid("unsafe CocoaPods archive")
		}
		p := strings.TrimSuffix(h.Name, "/")
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." {
				return Archive{}, invalid("unsafe CocoaPods archive path")
			}
		}
		if seen[p] {
			return Archive{}, invalid("duplicate CocoaPods archive entry")
		}
		seen[p] = true
		if h.Typeflag != tar.TypeReg && h.Typeflag != 0 && h.Typeflag != tar.TypeDir {
			return Archive{}, invalid("CocoaPods archive contains a link or special entry")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if strings.HasSuffix(p, ".podspec") || strings.HasSuffix(p, ".podspec.json") {
			if (p != name+".podspec.json" && p != name+".podspec") || spec != nil ||
				h.Size > maxSpecBytes {
				return Archive{}, invalid("CocoaPods archive needs one root podspec")
			}
			data, readErr := io.ReadAll(io.LimitReader(tr, maxSpecBytes+1))
			if readErr != nil || int64(len(data)) != h.Size {
				return Archive{}, invalid("truncated CocoaPods podspec")
			}
			if p == name+".podspec" {
				data, readErr = staticRubySpec(data)
				if readErr != nil {
					return Archive{}, fmt.Errorf("invalid static CocoaPods podspec: %w", readErr)
				}
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(data, &fields) != nil || len(fields) == 0 {
				return Archive{}, invalid("invalid CocoaPods podspec JSON")
			}
			var gotName, gotVersion string
			if json.Unmarshal(fields["name"], &gotName) != nil ||
				json.Unmarshal(fields["version"], &gotVersion) != nil ||
				gotName != name || gotVersion != version {
				return Archive{}, invalid("CocoaPods podspec identity differs from archive path")
			}
			spec = append([]byte(nil), data...)
			continue
		}
		if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
			return Archive{}, invalid("truncated CocoaPods archive member")
		}
	}
	if spec == nil {
		return Archive{}, invalid("CocoaPods archive lacks a root podspec")
	}
	var extra [1]byte
	if n, err := gz.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return Archive{}, invalid("extra CocoaPods archive data or invalid gzip trailer")
	}
	return Archive{Name: name, Version: version, Spec: spec}, nil
}

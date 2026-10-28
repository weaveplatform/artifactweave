// Package nix validates Nix binary-cache metadata and archive integrity.
package nix

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	MaxMetadataBytes       = 1 << 20
	MaxArchiveBytes  int64 = 512 << 20
	MaxNarBytes      int64 = 2 << 30
	CacheInfo              = "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n"
	alphabet               = "0123456789abcdfghijklmnpqrsvwxyz"
)

var (
	ErrInvalid = errors.New("nix: invalid binary cache content")
	storeName  = regexp.MustCompile(
		`^[0123456789abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]{1,211}$`,
	)
	keyName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
)

// Info holds the fields covered by a Nix signature and the archive's transport identity.
type Info struct {
	StorePath   string   `json:"store_path"`
	URL         string   `json:"url"`
	Compression string   `json:"compression"`
	FileHash    string   `json:"file_hash"`
	FileSize    int64    `json:"file_size"`
	NarHash     string   `json:"nar_hash"`
	NarSize     int64    `json:"nar_size"`
	References  []string `json:"references"`
	Deriver     string   `json:"deriver"`
	Signatures  []string `json:"signatures"`
}

func IsInfoPath(path string) bool {
	return len(path) == 40 && strings.HasSuffix(path, ".narinfo") && validBase32(path[:32])
}

func validBase32(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return s != ""
}

// ArchivePath admits only relative content-addressed archive names, never URLs.
func ArchivePath(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, "nar/")
	if !ok || strings.ContainsAny(name, "/%?#\\") {
		return "", false
	}
	hash, suffix, ok := strings.Cut(name, ".nar")
	if !ok {
		return "", false
	}
	if _, err := parseHash("sha256:" + hash); err != nil {
		return "", false
	}
	switch suffix {
	case "":
		return "none", true
	case ".xz":
		return "xz", true
	case ".bz2":
		return "bzip2", true
	case ".gz":
		return "gzip", true
	case ".zst":
		return "zstd", true
	}
	return "", false
}

func Parse(data []byte, path string) (Info, error) {
	var info Info
	if !IsInfoPath(path) || len(data) == 0 || len(data) > MaxMetadataBytes ||
		data[len(data)-1] != '\n' {
		return info, ErrInvalid
	}
	fields, signatures, err := parseFields(data)
	if err != nil {
		return info, err
	}
	info.Signatures = signatures
	info.StorePath = fields["StorePath"]
	base, ok := strings.CutPrefix(info.StorePath, "/nix/store/")
	if !ok || !storeName.MatchString(base) || base[:32]+".narinfo" != path {
		return info, ErrInvalid
	}
	info.URL = fields["URL"]
	compression, ok := ArchivePath(info.URL)
	if !ok {
		return info, ErrInvalid
	}
	info.Compression = fields["Compression"]
	if info.Compression == "" {
		info.Compression = "bzip2"
	}
	if compression != info.Compression {
		return info, ErrInvalid
	}
	info.NarHash, err = parseHash(fields["NarHash"])
	if err != nil {
		return info, err
	}
	info.FileHash, err = parseHash(fields["FileHash"])
	if err != nil {
		return info, err
	}
	info.NarSize, err = strconv.ParseInt(fields["NarSize"], 10, 64)
	if err != nil || info.NarSize <= 0 || info.NarSize > MaxNarBytes {
		return info, ErrInvalid
	}
	info.FileSize, err = strconv.ParseInt(fields["FileSize"], 10, 64)
	if err != nil || info.FileSize <= 0 || info.FileSize > MaxArchiveBytes {
		return info, ErrInvalid
	}
	filename := strings.Split(strings.TrimPrefix(info.URL, "nar/"), ".nar")[0]
	filenameHash, err := parseHash("sha256:" + filename)
	if err != nil || filenameHash != info.FileHash {
		return info, ErrInvalid
	}
	info.References = strings.Fields(fields["References"])
	if len(info.References) > 4096 {
		return info, ErrInvalid
	}
	slices.Sort(info.References)
	for i, ref := range info.References {
		if !storeName.MatchString(ref) || i > 0 && ref == info.References[i-1] {
			return info, ErrInvalid
		}
	}
	info.Deriver = fields["Deriver"]
	if info.Deriver != "" && info.Deriver != "unknown-deriver" &&
		!storeName.MatchString(info.Deriver) {
		return info, ErrInvalid
	}
	// CA is not signature-covered. Do not forward a publisher's unverified
	// content-address assertion, which clients can use to bypass signature trust.
	return info, nil
}

func parseFields(data []byte) (map[string]string, []string, error) {
	var signatures []string
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || strings.ContainsAny(value, "\x00\r\n") {
			return nil, nil, ErrInvalid
		}
		if key == "Sig" {
			name, signature, found := strings.Cut(value, ":")
			decoded, err := base64.StdEncoding.DecodeString(signature)
			if !found || !keyName.MatchString(name) || err != nil ||
				len(decoded) != ed25519.SignatureSize ||
				len(signatures) >= 32 {
				return nil, nil, ErrInvalid
			}
			signatures = append(signatures, value)
			continue
		}
		if _, exists := fields[key]; exists {
			return nil, nil, ErrInvalid
		}
		switch key {
		case "StorePath",
			"URL",
			"Compression",
			"FileHash",
			"FileSize",
			"NarHash",
			"NarSize",
			"References",
			"Deriver",
			"CA":
		default:
			return nil, nil, ErrInvalid
		}
		fields[key] = value
	}
	return fields, signatures, nil
}

func parseHash(value string) (string, error) {
	value, ok := strings.CutPrefix(value, "sha256:")
	if !ok {
		return "", ErrInvalid
	}
	if len(value) == 64 {
		b, err := hex.DecodeString(value)
		if err != nil {
			return "", ErrInvalid
		}
		return hex.EncodeToString(b), nil
	}
	if len(value) != 52 || !validBase32(value) {
		return "", ErrInvalid
	}
	var raw [32]byte
	for i := range len(value) {
		v := strings.IndexByte(alphabet, value[len(value)-1-i])
		bit := i * 5
		if bit == 255 && v > 1 {
			return "", ErrInvalid
		}
		raw[bit/8] |= byte((v << uint(bit%8)) & 255)
		if bit%8 > 3 && bit/8+1 < len(raw) {
			raw[bit/8+1] |= byte((v >> uint(8-bit%8)) & 255)
		}
	}
	return hex.EncodeToString(raw[:]), nil
}

// Nix32 encodes bytes in the bit order used by Nix, distinct from RFC 4648.
func Nix32(raw []byte) string {
	n := (len(raw)*8 + 4) / 5
	out := make([]byte, n)
	for i := range n {
		bit := i * 5
		v := uint(raw[bit/8]) >> uint(bit%8)
		if bit/8+1 < len(raw) {
			v |= uint(raw[bit/8+1]) << uint(8-bit%8)
		}
		out[n-1-i] = alphabet[v&31]
	}
	return string(out)
}

func nativeHash(
	hexed string,
) string {
	b, _ := hex.DecodeString(hexed)
	return "sha256:" + Nix32(b)
}

func (i Info) Fingerprint() []byte {
	refs := make([]string, len(i.References))
	for n, ref := range i.References {
		refs[n] = "/nix/store/" + ref
	}
	return []byte(
		"1;" + i.StorePath + ";" + nativeHash(
			i.NarHash,
		) + ";" + strconv.FormatInt(
			i.NarSize,
			10,
		) + ";" + strings.Join(
			refs,
			",",
		),
	)
}

func (i *Info) Sign(alias string, key ed25519.PrivateKey) error {
	if !keyName.MatchString(alias) || len(key) != ed25519.PrivateKeySize {
		return ErrInvalid
	}
	sig := alias + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(key, i.Fingerprint()))
	if !slices.Contains(i.Signatures, sig) {
		i.Signatures = append(i.Signatures, sig)
	}
	return nil
}

func (i Info) Verify(alias string, key ed25519.PublicKey) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	for _, sig := range i.Signatures {
		name, data, ok := strings.Cut(sig, ":")
		if ok && name == alias {
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err == nil && ed25519.Verify(key, i.Fingerprint(), decoded) {
				return true
			}
		}
	}
	return false
}

// Encode can rewrite the relative archive URL without changing the signed fingerprint.
func (i Info) Encode(url string) []byte {
	var b strings.Builder
	fmt.Fprintf(
		&b,
		"StorePath: %s\nURL: %s\nCompression: %s\nFileHash: %s\nFileSize: %d\nNarHash: %s\nNarSize: %d\nReferences: %s\n",
		i.StorePath,
		url,
		i.Compression,
		nativeHash(i.FileHash),
		i.FileSize,
		nativeHash(i.NarHash),
		i.NarSize,
		strings.Join(i.References, " "),
	)
	if i.Deriver != "" {
		fmt.Fprintf(&b, "Deriver: %s\n", i.Deriver)
	}
	for _, sig := range i.Signatures {
		fmt.Fprintf(&b, "Sig: %s\n", sig)
	}
	return []byte(b.String())
}

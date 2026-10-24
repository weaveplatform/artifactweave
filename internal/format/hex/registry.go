package hex

import (
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"sort"

	"google.golang.org/protobuf/encoding/protowire"
)

const maxRegistryBytes = 16 << 20

var errRegistry = errors.New("invalid Hex registry resource")

// RegistryPackage is the verified content of /packages/NAME. Release checksums
// are binary SHA-256 digests, exactly as Hex registry v2 prescribes.
type RegistryPackage struct {
	Name       string
	Repository string
	Releases   []Package
}

func ParsePublicKey(value string) (*rsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(value))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errRegistry
	}
	var key any
	var err error
	switch block.Type {
	case "PUBLIC KEY":
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	case "RSA PUBLIC KEY":
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, errRegistry
	}
	pub, ok := key.(*rsa.PublicKey)
	if err != nil || !ok || pub.N.BitLen() < 2048 {
		return nil, errRegistry
	}
	return pub, nil
}

func PublicKeyPEM(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "PUBLIC KEY", Bytes: mustPublicDER(&key.PublicKey),
	}))
}

func mustPublicDER(key *rsa.PublicKey) []byte {
	b, _ := x509.MarshalPKIXPublicKey(key)
	return b
}

func EncodePackage(reg RegistryPackage, private *rsa.PrivateKey) ([]byte, error) {
	if !ValidName(reg.Name) || reg.Repository == "" || private == nil {
		return nil, errRegistry
	}
	var payload []byte
	for _, release := range reg.Releases {
		if release.Name != reg.Name || !ValidVersion(release.Version) ||
			release.InnerChecksum == [32]byte{} || release.OuterChecksum == [32]byte{} {
			return nil, errRegistry
		}
		var row []byte
		row = appendString(row, 1, release.Version)
		row = appendBytes(row, 2, release.InnerChecksum[:])
		for _, dep := range release.Requirements {
			if !ValidName(dep.Package) || dep.Requirement == "" {
				return nil, errRegistry
			}
			var item []byte
			item = appendString(item, 1, dep.Package)
			item = appendString(item, 2, dep.Requirement)
			item = appendBool(item, 3, dep.Optional)
			if dep.App != "" && dep.App != dep.Package {
				item = appendString(item, 4, dep.App)
			}
			if dep.Repository != "" {
				item = appendString(item, 5, dep.Repository)
			}
			row = appendBytes(row, 3, item)
		}
		row = appendBytes(row, 5, release.OuterChecksum[:])
		payload = appendBytes(payload, 1, row)
	}
	payload = appendString(payload, 2, reg.Name)
	payload = appendString(payload, 3, reg.Repository)
	return sign(payload, private)
}

func DecodePackage(body []byte, key *rsa.PublicKey) (RegistryPackage, error) {
	var reg RegistryPackage
	payload, err := verify(body, key)
	if err != nil {
		return reg, err
	}
	var nameSeen, repoSeen bool
	err = fields(payload, func(num protowire.Number, typ protowire.Type, val []byte) error {
		switch num {
		case 1:
			if typ != protowire.BytesType {
				return errRegistry
			}
			release, err := decodeRelease(val)
			if err != nil {
				return err
			}
			reg.Releases = append(reg.Releases, release)
		case 2:
			if typ != protowire.BytesType || nameSeen {
				return errRegistry
			}
			reg.Name, nameSeen = string(val), true
		case 3:
			if typ != protowire.BytesType || repoSeen {
				return errRegistry
			}
			reg.Repository, repoSeen = string(val), true
		}
		return nil
	})
	if err != nil || !nameSeen || !repoSeen || !ValidName(reg.Name) ||
		reg.Repository == "" || len(reg.Releases) > 10000 {
		return RegistryPackage{}, errRegistry
	}
	seen := map[string]bool{}
	for i := range reg.Releases {
		reg.Releases[i].Name = reg.Name
		if seen[reg.Releases[i].Version] {
			return RegistryPackage{}, errRegistry
		}
		seen[reg.Releases[i].Version] = true
	}
	return reg, nil
}

func decodeRelease(data []byte) (Package, error) {
	var out Package
	var version, inner, outer bool
	err := fields(data, func(num protowire.Number, typ protowire.Type, val []byte) error {
		switch num {
		case 1:
			if typ != protowire.BytesType || version {
				return errRegistry
			}
			out.Version, version = string(val), true
		case 2:
			if typ != protowire.BytesType || len(val) != 32 || inner {
				return errRegistry
			}
			copy(out.InnerChecksum[:], val)
			inner = true
		case 3:
			if typ != protowire.BytesType {
				return errRegistry
			}
			dep, err := decodeDependency(val)
			if err != nil {
				return err
			}
			out.Requirements = append(out.Requirements, dep)
		case 5:
			if typ != protowire.BytesType || len(val) != 32 || outer {
				return errRegistry
			}
			copy(out.OuterChecksum[:], val)
			outer = true
		}
		return nil
	})
	if err != nil || !version || !inner || !outer || !ValidVersion(out.Version) ||
		len(out.Requirements) > 1000 {
		return Package{}, errRegistry
	}
	return out, nil
}

func decodeDependency(data []byte) (Requirement, error) {
	var out Requirement
	var name, requirement bool
	err := fields(data, func(num protowire.Number, typ protowire.Type, val []byte) error {
		switch num {
		case 1:
			if typ != protowire.BytesType || name {
				return errRegistry
			}
			out.Package, name = string(val), true
		case 2:
			if typ != protowire.BytesType || requirement {
				return errRegistry
			}
			out.Requirement, requirement = string(val), true
		case 3:
			if typ != protowire.VarintType {
				return errRegistry
			}
			n, nBytes := protowire.ConsumeVarint(val)
			if nBytes < 0 || n > 1 {
				return errRegistry
			}
			out.Optional = n == 1
		case 4:
			if typ != protowire.BytesType {
				return errRegistry
			}
			out.App = string(val)
		case 5:
			if typ != protowire.BytesType {
				return errRegistry
			}
			out.Repository = string(val)
		}
		return nil
	})
	if err != nil || !name || !requirement || !ValidName(out.Package) ||
		out.Requirement == "" || len(out.Requirement) > 256 ||
		out.App != "" && !ValidName(out.App) {
		return Requirement{}, errRegistry
	}
	return out, nil
}

func EncodeNames(repository string, names []string, private *rsa.PrivateKey) ([]byte, error) {
	if repository == "" || private == nil {
		return nil, errRegistry
	}
	sort.Strings(names)
	var payload []byte
	previous := ""
	for _, name := range names {
		if !ValidName(name) || name == previous {
			return nil, errRegistry
		}
		payload = appendBytes(payload, 1, appendString(nil, 1, name))
		previous = name
	}
	payload = appendString(payload, 2, repository)
	return sign(payload, private)
}

func EncodeVersions(
	repository string,
	packages []RegistryPackage,
	private *rsa.PrivateKey,
) ([]byte, error) {
	if repository == "" || private == nil {
		return nil, errRegistry
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Name < packages[j].Name })
	var payload []byte
	previous := ""
	for _, pkg := range packages {
		if !ValidName(pkg.Name) || pkg.Name == previous {
			return nil, errRegistry
		}
		item := appendString(nil, 1, pkg.Name)
		for _, release := range pkg.Releases {
			if !ValidVersion(release.Version) {
				return nil, errRegistry
			}
			item = appendString(item, 2, release.Version)
		}
		payload = appendBytes(payload, 1, item)
		previous = pkg.Name
	}
	payload = appendString(payload, 2, repository)
	return sign(payload, private)
}

// DecodeNames verifies a signed upstream /names resource before it is used
// to build a new resource under this repository's own signing key.
func DecodeNames(body []byte, key *rsa.PublicKey) (string, []string, error) {
	payload, err := verify(body, key)
	if err != nil {
		return "", nil, err
	}
	var repository string
	var names []string
	err = fields(payload, func(num protowire.Number, typ protowire.Type, val []byte) error {
		if typ != protowire.BytesType {
			return errRegistry
		}
		switch num {
		case 1:
			var name string
			if err := fields(val, func(n protowire.Number, t protowire.Type, v []byte) error {
				if n == 1 && t == protowire.BytesType && name == "" {
					name = string(v)
					return nil
				}
				return errRegistry
			}); err != nil || !ValidName(name) {
				return errRegistry
			}
			names = append(names, name)
		case 2:
			if repository != "" {
				return errRegistry
			}
			repository = string(val)
		}
		return nil
	})
	if err != nil || repository == "" || len(names) > 100000 {
		return "", nil, errRegistry
	}
	return repository, names, nil
}

// DecodeVersions verifies a signed upstream /versions resource.
func DecodeVersions(body []byte, key *rsa.PublicKey) (string, []RegistryPackage, error) {
	payload, err := verify(body, key)
	if err != nil {
		return "", nil, err
	}
	var repository string
	var packages []RegistryPackage
	err = fields(payload, func(num protowire.Number, typ protowire.Type, val []byte) error {
		if typ != protowire.BytesType {
			return errRegistry
		}
		switch num {
		case 1:
			var pkg RegistryPackage
			if err := fields(val, func(n protowire.Number, t protowire.Type, v []byte) error {
				if t != protowire.BytesType {
					return errRegistry
				}
				switch n {
				case 1:
					if pkg.Name != "" {
						return errRegistry
					}
					pkg.Name = string(v)
				case 2:
					pkg.Releases = append(pkg.Releases, Package{Version: string(v)})
				}
				return nil
			}); err != nil || !ValidName(pkg.Name) {
				return errRegistry
			}
			for _, release := range pkg.Releases {
				if !ValidVersion(release.Version) {
					return errRegistry
				}
			}
			packages = append(packages, pkg)
		case 2:
			if repository != "" {
				return errRegistry
			}
			repository = string(val)
		}
		return nil
	})
	if err != nil || repository == "" || len(packages) > 100000 {
		return "", nil, errRegistry
	}
	return repository, packages, nil
}

func sign(payload []byte, key *rsa.PrivateKey) ([]byte, error) {
	if key == nil || key.N.BitLen() < 2048 || len(payload) > maxRegistryBytes {
		return nil, errRegistry
	}
	digest := sha512.Sum512(payload)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA512, digest[:])
	if err != nil {
		return nil, fmt.Errorf("hex: sign registry: %w", err)
	}
	envelope := appendBytes(nil, 1, payload)
	envelope = appendBytes(envelope, 2, sig)
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write(envelope); err != nil {
		return nil, fmt.Errorf("hex: compress registry: %w", err)
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("hex: close registry compression: %w", err)
	}
	return buf.Bytes(), nil
}

func verify(body []byte, key *rsa.PublicKey) ([]byte, error) {
	if len(body) == 0 || len(body) > maxRegistryBytes || key == nil {
		return nil, errRegistry
	}
	z, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, errRegistry
	}
	defer func() { _ = z.Close() }()
	envelope, err := io.ReadAll(io.LimitReader(z, maxRegistryBytes+1))
	if err != nil || len(envelope) > maxRegistryBytes {
		return nil, errRegistry
	}
	var payload, signature []byte
	err = fields(envelope, func(num protowire.Number, typ protowire.Type, val []byte) error {
		if typ != protowire.BytesType {
			return errRegistry
		}
		switch num {
		case 1:
			if payload != nil {
				return errRegistry
			}
			payload = val
		case 2:
			if signature != nil {
				return errRegistry
			}
			signature = val
		}
		return nil
	})
	if err != nil || len(payload) == 0 || len(signature) == 0 {
		return nil, errRegistry
	}
	digest := sha512.Sum512(payload)
	if rsa.VerifyPKCS1v15(key, crypto.SHA512, digest[:], signature) != nil {
		return nil, errRegistry
	}
	return payload, nil
}

func appendBytes(dst []byte, field protowire.Number, body []byte) []byte {
	dst = protowire.AppendTag(dst, field, protowire.BytesType)
	return protowire.AppendBytes(dst, body)
}

func appendString(dst []byte, field protowire.Number, value string) []byte {
	return appendBytes(dst, field, []byte(value))
}

func appendBool(dst []byte, field protowire.Number, value bool) []byte {
	dst = protowire.AppendTag(dst, field, protowire.VarintType)
	if value {
		return protowire.AppendVarint(dst, 1)
	}
	return protowire.AppendVarint(dst, 0)
}

func fields(data []byte, visit func(protowire.Number, protowire.Type, []byte) error) error {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 || num <= 0 {
			return errRegistry
		}
		data = data[n:]
		var val []byte
		switch typ {
		case protowire.BytesType:
			var used int
			val, used = protowire.ConsumeBytes(data)
			if used < 0 {
				return errRegistry
			}
			data = data[used:]
		case protowire.VarintType:
			_, used := protowire.ConsumeVarint(data)
			if used < 0 {
				return errRegistry
			}
			val, data = data[:used], data[used:]
		default:
			used := protowire.ConsumeFieldValue(num, typ, data)
			if used < 0 {
				return errRegistry
			}
			data = data[used:]
			continue
		}
		if err := visit(num, typ, val); err != nil {
			return err
		}
	}
	return nil
}

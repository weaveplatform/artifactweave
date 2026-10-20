package terraform

import (
	"regexp"
	"strings"

	"golang.org/x/net/idna"
)

// semverPattern is semantic versioning 2.0.0's own expression: a
// MAJOR.MINOR.PATCH core without leading zeros, then optional pre-release
// and build parts.
var semverPattern = regexp.MustCompile(`^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)` +
	`(?:-(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*)?` +
	`(?:\+[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*)?$`)

// Terraform's rules for the parts of a module address
// (terraform-registry-address v0.5.0, module.go): a namespace or name of 1
// to 64 letters, digits, "-" and "_", starting and ending with a letter or
// digit, and a target system of 1 to 64 lower-case letters and digits.
var (
	moduleNamePattern   = regexp.MustCompile(`^[0-9A-Za-z](?:[0-9A-Za-z-_]{0,62}[0-9A-Za-z])?$`)
	moduleSystemPattern = regexp.MustCompile(`^[0-9a-z]{1,64}$`)
)

// Module is a module version's zip as the index reads its path,
// <ns>/<name>/<system>/<version>.zip (decision 0040). The namespace, name
// and system are kept as written: Terraform keeps the case of the first two
// and sends them as written.
type Module struct {
	Namespace, Name, System string
	// Version is the semantic version, without the "v" a file name may
	// spell it with (jf terraform publish's tags carry one).
	Version string
	// V reports a file name that spells the version with a "v".
	V bool
}

// Package is the module's name in the package index,
// <ns>/<name>/<system>.
func (m Module) Package() string { return m.Namespace + "/" + m.Name + "/" + m.System }

// Path is where the zip is stored.
func (m Module) Path() string {
	v := m.Version
	if m.V {
		v = "v" + v
	}
	return m.Package() + "/" + v + ".zip"
}

// ParseModule reads a module zip's repository-relative path; ok is false
// for any other file, which a MODULE repository stores without indexing it.
func ParseModule(p string) (Module, bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 4 {
		return Module{}, false
	}
	stem, ok := strings.CutSuffix(parts[3], ".zip")
	if !ok || !moduleNamePattern.MatchString(parts[0]) ||
		!moduleNamePattern.MatchString(parts[1]) || !moduleSystemPattern.MatchString(parts[2]) {
		return Module{}, false
	}
	m := Module{Namespace: parts[0], Name: parts[1], System: parts[2], Version: stem}
	if v, cut := strings.CutPrefix(stem, "v"); cut {
		m.Version, m.V = v, true
	}
	if !semverPattern.MatchString(m.Version) {
		return Module{}, false
	}
	return m, true
}

// Kind is the part a file plays in a version of a Terraform provider
// package.
type Kind int

// The files of a Terraform provider package's release (decision 0040).
const (
	// KindArchive is one platform's zip,
	// terraform-provider-<type>_<version>_<os>_<arch>.zip, which the
	// registry protocol calls a package and the network mirror an archive.
	KindArchive Kind = iota + 1
	// KindSums is terraform-provider-<type>_<version>_SHA256SUMS.
	KindSums
	// KindSignature is the detached GPG signature of the SHA256SUMS,
	// terraform-provider-<type>_<version>_SHA256SUMS.sig.
	KindSignature
	// KindManifest is terraform-provider-<type>_<version>_manifest.json.
	KindManifest
)

// releasePrefix starts the name of every file of a Terraform provider
// package's release.
const releasePrefix = "terraform-provider-"

// The file name suffixes, after <type>_<version>_, of the files other than
// the archives.
var releaseSuffixes = map[string]Kind{
	"SHA256SUMS":     KindSums,
	"SHA256SUMS.sig": KindSignature,
	"manifest.json":  KindManifest,
}

// platformPart is an os or arch of an archive's file name.
var platformPart = regexp.MustCompile(`^[a-z0-9]+$`)

// ProviderPackageFile is a file of a Terraform provider package's version
// as the index reads its path, [<folders>/]<ns>/<type>/<version>/<file>
// (decision 0040).
type ProviderPackageFile struct {
	// Namespace and Type are the folders' names as Terraform normalises an
	// address, which it sends in lower case: the index's package is
	// <ns>/<type> in that form, whatever case the folders are stored in.
	Namespace, Type string
	Version         string
	Kind            Kind
	// OS and Arch are an archive's platform.
	OS, Arch string
}

// Package is the Terraform provider package's name in the package index,
// <ns>/<type>.
func (f ProviderPackageFile) Package() string { return f.Namespace + "/" + f.Type }

// Platform is an archive's "<os>_<arch>", the key the network mirror lists
// it under.
func (f ProviderPackageFile) Platform() string { return f.OS + "_" + f.Arch }

// role is the place f holds in its version: one per platform for the
// archives, and one each for the SHA256SUMS, the signature and the
// manifest. Two paths that claim the same role in one version, under
// different folders or folder cases, are resolved by path order.
func (f ProviderPackageFile) role() string {
	if f.Kind == KindArchive {
		return "archive " + f.Platform()
	}
	return map[Kind]string{KindSums: "sums", KindSignature: "signature", KindManifest: "manifest"}[f.Kind]
}

// ParseProviderPackageFile reads the repository-relative path of a file of
// a Terraform provider package; ok is false for any other file, such as
// HashiCorp's key-specific _SHA256SUMS.<keyid>.sig, which a PROVIDER
// repository stores without indexing it. The type and version in the file
// name must be the folders', the type compared without regard to case.
// Folders before the namespace are ignored, as in JFrog's example, which
// adds one.
func ParseProviderPackageFile(p string) (ProviderPackageFile, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 4 {
		return ProviderPackageFile{}, false
	}
	parts = parts[len(parts)-4:]
	ns, typ, version, name := parts[0], parts[1], parts[2], parts[3]
	rest, ok := strings.CutPrefix(name, releasePrefix)
	if !ok || !semverPattern.MatchString(version) {
		return ProviderPackageFile{}, false
	}
	// A type holds no "_" and a version none either, so the first two "_"
	// end them.
	nameType, rest, _ := strings.Cut(rest, "_")
	nameVersion, suffix, _ := strings.Cut(rest, "_")
	if !strings.EqualFold(nameType, typ) || nameVersion != version {
		return ProviderPackageFile{}, false
	}
	f := ProviderPackageFile{Version: version}
	if f.Namespace, ok = addressNamespace(ns); !ok {
		return ProviderPackageFile{}, false
	}
	if f.Type, ok = addressType(typ); !ok {
		return ProviderPackageFile{}, false
	}
	if k, ok := releaseSuffixes[suffix]; ok {
		f.Kind = k
		return f, true
	}
	platform, ok := strings.CutSuffix(suffix, ".zip")
	f.OS, f.Arch, _ = strings.Cut(platform, "_")
	if !ok || !platformPart.MatchString(f.OS) || !platformPart.MatchString(f.Arch) {
		return ProviderPackageFile{}, false
	}
	f.Kind = KindArchive
	return f, true
}

// addressNamespace and addressType check and normalise the namespace and
// type of a Terraform provider package's address as Terraform does
// (terraform-registry-address v0.5.0, provider.go, ParseProviderNamespace
// and ParseProviderPart), so the index's name is the one Terraform asks
// for: lower case, and a non-ASCII name in the Unicode form Terraform
// sends. A type is a DNS label: letters, digits and "-", which may not
// start or end it. A namespace may hold "_" too, but not at either end.
func addressNamespace(s string) (string, bool) {
	if !addressPart(s) || strings.HasPrefix(s, "_") || strings.HasSuffix(s, "_") ||
		strings.ContainsFunc(s, func(r rune) bool {
			return r < 128 && r != '-' && r != '_' && !isASCIIAlnum(r)
		}) {
		return "", false
	}
	out, err := namespaceProfile.ToUnicode(s)
	return out, err == nil
}

func addressType(s string) (string, bool) {
	if !addressPart(s) {
		return "", false
	}
	out, err := idna.Lookup.ToUnicode(s)
	return out, err == nil
}

// namespaceProfile is idna.Lookup without the strict domain name rules,
// which forbid "_": Terraform's own profile for namespaces.
var namespaceProfile = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.StrictDomainName(false))

// addressPart applies what Terraform checks of every part of an address
// before its own rules: not empty, no ".", and no "--".
func addressPart(s string) bool {
	return s != "" && !strings.Contains(s, ".") && !strings.Contains(s, "--")
}

func isASCIIAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

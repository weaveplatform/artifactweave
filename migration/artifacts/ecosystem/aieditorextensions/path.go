package aieditorextensions

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

// Serves reports whether t is the AI editor extensions package type.
func Serves(t repo.PackageType) bool { return t == repo.AIEditorExtensions }

// Identity is the case-insensitive publisher.extension name used by VS Code.
func Identity(raw string) (publisher, name string, ok bool) {
	publisher, name, ok = strings.Cut(raw, ".")
	if !ok || !Segment(publisher) || !Segment(name) || strings.Contains(name, ".") {
		return "", "", false
	}
	return strings.ToLower(publisher), strings.ToLower(name), true
}

// Segment reports whether raw is a valid publisher or extension name: 1 to
// 128 ASCII letters, digits, hyphens and underscores, starting with a
// letter or digit.
func Segment(raw string) bool {
	if raw == "" || len(raw) > 128 || raw == "." || raw == ".." {
		return false
	}
	for i, ch := range raw {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case i > 0 && (ch == '-' || ch == '_'):
		default:
			return false
		}
	}
	return true
}

// Version reports whether raw is an extension version: at most 64 bytes,
// three numeric parts of at most 10 digits each, then optionally a hyphen
// and a suffix of letters, digits, dots and hyphens, with no "..".
func Version(raw string) bool {
	if len(raw) == 0 || len(raw) > 64 || strings.Contains(raw, "..") {
		return false
	}
	parts := strings.SplitN(raw, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return false
	}
	for _, n := range core {
		if n == "" || len(n) > 10 {
			return false
		}
		for _, ch := range n {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return false
		}
		for _, ch := range parts[1] {
			if ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' ||
				ch >= 'a' && ch <= 'z' || ch == '.' || ch == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// ExtensionFolder is the lower-case folder, extensions/<publisher>/<name>,
// that holds an extension's cached assets and whose read permission an
// exact gallery query checks.
func ExtensionFolder(publisher, name string) string {
	return "extensions/" + strings.ToLower(publisher) + "/" + strings.ToLower(name)
}

// QueryPath is the cache path of a gallery query's answer,
// gallery/queries/<SHA-256 of body>.json, so distinct query bodies never
// share an answer.
func QueryPath(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("gallery/queries/%x.json", sum)
}

// AssetCachePath returns the cache path of one gallery asset of an
// extension version, <ExtensionFolder>/<version>/<file>, and reports
// whether the identity and version are valid and assetType is one of the
// nine gallery asset types it maps to a file (manifest.json, package.zip,
// signature.zip, vsixmanifest.xml, changelog.txt, details.txt,
// license.txt, icon-default.bin and icon-small.bin).
func AssetCachePath(publisher, name, version, assetType string) (string, bool) {
	if !Segment(publisher) || !Segment(name) || !Version(version) {
		return "", false
	}
	file := ""
	switch assetType {
	case "Microsoft.VisualStudio.Code.Manifest":
		file = "manifest.json"
	case "Microsoft.VisualStudio.Services.VSIXPackage":
		file = "package.zip"
	case "Microsoft.VisualStudio.Services.VsixSignature":
		file = "signature.zip"
	case "Microsoft.VisualStudio.Services.VsixManifest":
		file = "vsixmanifest.xml"
	case "Microsoft.VisualStudio.Services.Content.Changelog":
		file = "changelog.txt"
	case "Microsoft.VisualStudio.Services.Content.Details":
		file = "details.txt"
	case "Microsoft.VisualStudio.Services.Content.License":
		file = "license.txt"
	case "Microsoft.VisualStudio.Services.Icons.Default":
		file = "icon-default.bin"
	case "Microsoft.VisualStudio.Services.Icons.Small":
		file = "icon-small.bin"
	default:
		return "", false
	}
	return ExtensionFolder(publisher, name) + "/" + version + "/" + file, true
}

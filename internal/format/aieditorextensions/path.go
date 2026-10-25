// Package aieditorextensions owns VS Code gallery identities and cache paths.
package aieditorextensions

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func Serves(t repo.PackageType) bool { return t == repo.AIEditorExtensions }

// Identity is the case-insensitive publisher.extension name used by VS Code.
func Identity(raw string) (publisher, name string, ok bool) {
	publisher, name, ok = strings.Cut(raw, ".")
	if !ok || !Segment(publisher) || !Segment(name) || strings.Contains(name, ".") {
		return "", "", false
	}
	return strings.ToLower(publisher), strings.ToLower(name), true
}

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

func ExtensionFolder(publisher, name string) string {
	return "extensions/" + strings.ToLower(publisher) + "/" + strings.ToLower(name)
}

func QueryPath(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("gallery/queries/%x.json", sum)
}

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

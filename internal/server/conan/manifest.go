package conan

import (
	"encoding/hex"
	"strconv"
	"strings"

	conanfmt "github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
)

func parseManifest(data []byte) (map[string]string, error) {
	if len(data) == 0 || len(data) > maxManifest {
		return nil, apierror.New(400, "Invalid Conan manifest")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 || len(lines) > 10000 {
		return nil, apierror.New(400, "Invalid Conan manifest")
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64); err != nil {
		return nil, apierror.New(400, "Invalid Conan manifest timestamp")
	}
	entries := make(map[string]string, len(lines)-1)
	for _, line := range lines[1:] {
		name, sum, ok := strings.Cut(strings.TrimSpace(line), ": ")
		sum = strings.TrimSpace(sum)
		if !ok || !conanfmt.SafeFile(name) || name == conanfmt.Manifest || len(sum) != 32 {
			return nil, apierror.New(400, "Invalid Conan manifest entry")
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, apierror.New(400, "Duplicate Conan manifest entry")
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, apierror.New(400, "Invalid Conan manifest checksum")
		}
		entries[name] = strings.ToLower(sum)
	}
	return entries, nil
}

package nuget_test

import (
	"encoding/json"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
)

// The store decides which versions a NuGet search or autocomplete shows a
// whole-repository reader from the stored metadata's keys
// (port.NuGetShown), so they must be the keys Metadata writes: a renamed
// tag would leave the store's count reading a key no version has, and
// count every version as without a .nupkg (#1425).
func TestUnitShownKeysAreMetadataTags(t *testing.T) {
	doc, err := json.Marshal(nuget.Metadata{
		Version: "1.0.0-beta", Path: "a/a.1.0.0-beta.nupkg", SemVer2: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(doc, &keys); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		port.NuGetVersionKey: "1.0.0-beta",
		port.NuGetPathKey:    "a/a.1.0.0-beta.nupkg",
		port.NuGetSemVer2Key: true,
	} {
		if keys[key] != want {
			t.Errorf("metadata %s: key %q holds %v, want %v", doc, key, keys[key], want)
		}
	}
}

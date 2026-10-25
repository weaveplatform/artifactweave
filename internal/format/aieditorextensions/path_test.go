package aieditorextensions

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitNativeIdentityAndCachePaths(t *testing.T) {
	t.Parallel()
	if !Serves(repo.AIEditorExtensions) || Serves(repo.NPM) {
		t.Fatal("wrong format owner")
	}
	pub, name, ok := Identity("MS-VSCode.HexEditor")
	if !ok || pub != "ms-vscode" || name != "hexeditor" ||
		ExtensionFolder(pub, name) != "extensions/ms-vscode/hexeditor" {
		t.Fatalf("identity: %q %q %t", pub, name, ok)
	}
	for _, bad := range []string{"", "a", ".x", "a.", "a.x.y", "../x", "a/x.y", "a.%2f", "_a.x", "a._x", strings.Repeat("a", 129) + ".x"} {
		if _, _, ok := Identity(bad); ok {
			t.Errorf("admitted identity %q", bad)
		}
	}
	for _, good := range []string{"1.2.3", "1.2.3-beta.1"} {
		if !Version(good) {
			t.Errorf("rejected version %q", good)
		}
	}
	for _, bad := range []string{"", "1.2", "1.2.3/evil", "1.2.3..x", "1.2.3-", "1.2.a", strings.Repeat("1", 65)} {
		if Version(bad) {
			t.Errorf("admitted version %q", bad)
		}
	}
	if QueryPath([]byte("a")) == QueryPath([]byte("b")) ||
		!strings.HasPrefix(QueryPath([]byte("a")), "gallery/queries/") {
		t.Fatal("distinct gallery queries share a cache identity")
	}
	for _, asset := range []string{"Microsoft.VisualStudio.Code.Manifest", "Microsoft.VisualStudio.Services.VSIXPackage", "Microsoft.VisualStudio.Services.VsixSignature", "Microsoft.VisualStudio.Services.VsixManifest", "Microsoft.VisualStudio.Services.Content.Changelog", "Microsoft.VisualStudio.Services.Content.Details", "Microsoft.VisualStudio.Services.Content.License", "Microsoft.VisualStudio.Services.Icons.Default", "Microsoft.VisualStudio.Services.Icons.Small"} {
		if path, ok := AssetCachePath(
			pub,
			name,
			"1.2.3",
			asset,
		); !ok ||
			!strings.HasPrefix(path, "extensions/ms-vscode/hexeditor/1.2.3/") {
			t.Errorf("asset %q mapped to %q, %t", asset, path, ok)
		}
	}
	for _, tc := range []struct{ pub, name, version, asset string }{{"../x", "hexeditor", "1.2.3", "Microsoft.VisualStudio.Code.Manifest"}, {"ms-vscode", "..", "1.2.3", "Microsoft.VisualStudio.Code.Manifest"}, {"ms-vscode", "hexeditor", "bad", "Microsoft.VisualStudio.Code.Manifest"}, {"ms-vscode", "hexeditor", "1.2.3", "../VSIXPackage"}} {
		if _, ok := AssetCachePath(tc.pub, tc.name, tc.version, tc.asset); ok {
			t.Errorf("admitted asset %+v", tc)
		}
	}
}

func TestUnitGalleryCacheClassification(t *testing.T) {
	t.Parallel()
	h := Handler{}
	for _, path := range []string{"gallery/queries/abc.json", "extensions/ms-vscode/hexeditor/1.2.3/manifest.json", "extensions/ms-vscode/hexeditor/1.2.3/signature.zip"} {
		if h.Classify(repo.Config{}, path) != format.ClassMetadata {
			t.Errorf("metadata %q", path)
		}
	}
	if h.Classify(
		repo.Config{},
		"extensions/ms-vscode/hexeditor/1.2.3/package.zip",
	) != format.ClassUnique {
		t.Fatal("VSIX classification")
	}
}

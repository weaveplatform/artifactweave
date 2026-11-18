package format

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitCachePolicyBinding(t *testing.T) {
	c := repo.Config{Key: "remote", Class: repo.Remote}
	identity := model.SourceIdentity{Kind: model.ExternalHTTP, ID: "remote", Repository: "incarnation", Generation: "generation"}
	p, err := NewCachePolicy(c, identity, "demo/1/file.whl", "files/file.whl")
	if err != nil || p.Empty() {
		t.Fatal(p, err)
	}
	if err := p.Validate(c, identity, "demo/1/file.whl"); err != nil {
		t.Fatal(err)
	}
	w := Write{Op: OpDeploy, Path: "demo/1/file.whl", Cached: true, CachePolicy: p}
	if path, err := w.PatternPath(c); err != nil || path != "files/file.whl" || w.Path != "demo/1/file.whl" {
		t.Fatal(path, err, w.Path)
	}
	for _, change := range []string{"repository", "source", "target", "class", "not-cached", "copy", "move", "checksum"} {
		t.Run(change, func(t *testing.T) {
			other, source, target, write := c, identity, w.Path, w
			switch change {
			case "repository":
				other.Key = "other"
			case "source":
				source.Generation = "new"
			case "target":
				target, write.Path = "other/file.whl", "other/file.whl"
			case "class":
				other.Class = repo.Local
			case "not-cached":
				write.Cached = false
			case "copy":
				write.Op = OpCopy
			case "move":
				write.Op = OpMove
			case "checksum":
				write.Op = OpChecksum
			}
			if change == "source" || change == "target" || change == "repository" || change == "class" {
				if err := p.Validate(other, source, target); !errors.Is(err, kernel.ErrInvalid) {
					t.Fatal("binding expanded", err)
				}
			}
			if change != "source" {
				if _, err := write.PatternPath(other); !errors.Is(err, kernel.ErrInvalid) {
					t.Fatal("write expanded", err)
				}
			}
		})
	}
	zero := Write{Op: OpDeploy, Path: "ordinary", Cached: false}
	if path, err := zero.PatternPath(repo.Config{Class: repo.Local}); err != nil || path != zero.Path || !(CachePolicy{}).Empty() {
		t.Fatal("ordinary write changed", path, err)
	}
	if err := (CachePolicy{}).Validate(c, identity, "ordinary"); err != nil {
		t.Fatal(err)
	}
}

func TestUnitCachePolicyRejectsMalformedBindings(t *testing.T) {
	c := repo.Config{Key: "remote", Class: repo.Remote}
	id := model.SourceIdentity{Kind: model.ExternalHTTP, ID: "remote", Repository: "incarnation", Generation: "generation"}
	for _, path := range []string{"", "/absolute", "trailing/", "a//b", "../escape", "a\nb", "a\rb"} {
		t.Run(path, func(t *testing.T) {
			if _, err := NewCachePolicy(c, id, "target", path); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("policy path admitted", path, err)
			}
			if _, err := NewCachePolicy(c, id, path, "policy"); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("target admitted", path, err)
			}
		})
	}
	if _, err := NewCachePolicy(c, model.SourceIdentity{}, "target", "policy"); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("unbound source", err)
	}
	if _, err := NewCachePolicy(repo.Config{Class: repo.Local}, id, "target", "policy"); !errors.Is(err, kernel.ErrInvalid) {
		t.Fatal("non-remote", err)
	}
}

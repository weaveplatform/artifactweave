package format

import (
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// CachePolicy binds an authorized source's policy namespace to one canonical
// cache target. It changes only pattern selection, never the item's identity or
// the authority checks that fence its publication. Its zero value has no override.
type CachePolicy struct {
	repository string
	target     string
	policy     string
	source     model.SourceIdentity
}

// NewCachePolicy validates the binding's names. The source service creates it
// only after authorizing the read; this value grants no permission on its own.
func NewCachePolicy(c repo.Config, identity model.SourceIdentity, target, policy string) (CachePolicy, error) {
	p := CachePolicy{repository: c.Key, target: target, policy: policy, source: identity}
	if c.Class != repo.Remote || c.Key == "" || identity.Kind != model.ExternalHTTP || identity.ID == "" || identity.Repository == "" || identity.Generation == "" ||
		!cachePolicyPath(target) || !cachePolicyPath(policy) {
		return CachePolicy{}, fmt.Errorf("cache policy binding: %w", kernel.ErrInvalid)
	}
	return p, nil
}

func cachePolicyPath(path string) bool {
	if path == "" || path != strings.Trim(path, "/") || strings.ContainsAny(path, "\r\n") {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil
}

// Empty reports whether this is the ordinary, canonical-path policy.
func (p CachePolicy) Empty() bool { return p == (CachePolicy{}) }

// Validate keeps a binding on the source incarnation and target it was minted
// for. The cache publisher still has to run its current-authority fence.
func (p CachePolicy) Validate(c repo.Config, identity model.SourceIdentity, target string) error {
	if p.Empty() {
		return nil
	}
	if c.Class != repo.Remote || c.Key != p.repository || identity != p.source || target != p.target {
		return fmt.Errorf("cache policy target: %w", kernel.ErrInvalid)
	}
	return nil
}

// PatternPath is the source policy path for its bound cached deploy, otherwise
// the normal write path. Format inspection and storage keep using Write.Path.
func (w Write) PatternPath(c repo.Config) (string, error) {
	p := w.CachePolicy
	if p.Empty() {
		return w.Path, nil
	}
	if !w.Cached || w.Op != OpDeploy || c.Class != repo.Remote || c.Key != p.repository || w.Path != p.target {
		return "", fmt.Errorf("cache policy write: %w", kernel.ErrInvalid)
	}
	return p.policy, nil
}

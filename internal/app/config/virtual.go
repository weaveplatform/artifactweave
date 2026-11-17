package config

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// The merged virtual metadata cache's default bounds, the cache's own.
const (
	DefaultVirtualCacheEntries          = virtual.DefaultCacheEntries
	DefaultVirtualCacheSize    ByteSize = ByteSize(virtual.DefaultCacheBytes)
)

// Virtual holds the virtual repositories' options (decision 0008).
type Virtual struct {
	MetadataCache VirtualMetadataCache `yaml:"metadata_cache"`
}

// VirtualMetadataCache bounds the memory each node keeps merged virtual
// metadata in for virtualRetrievalCachePeriodSecs (decision 0008, R8). 7.x
// keeps that metadata in each virtual repository's storage with no bound;
// artifactweave keeps it in memory, so an operator sizes it.
type VirtualMetadataCache struct {
	// MaxEntries is the most merged documents a node keeps
	// (virtual.metadata_cache.max_entries).
	MaxEntries int `yaml:"max_entries"`
	// MaxSize is the most bytes they hold (virtual.metadata_cache.max_size).
	MaxSize ByteSize `yaml:"max_size"`
}

// setVirtual assigns one key under virtual; sub is the key without that
// prefix.
func setVirtual(v *Virtual, sub, key, value string) error {
	switch sub {
	case "metadata_cache.max_entries":
		return setInt(&v.MetadataCache.MaxEntries, key, value)
	case "metadata_cache.max_size":
		return setByteSize(&v.MetadataCache.MaxSize, key, value)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
}

// validate refuses an empty cache: with no room, every merge would be
// built for every request, and a period set on a repository would mean
// nothing.
func (v Virtual) validate() []error {
	var errs []error
	if n := v.MetadataCache.MaxEntries; n < 1 {
		errs = append(errs, fmt.Errorf(
			"%w: virtual.metadata_cache.max_entries %d must be at least 1", ErrInvalidValue, n))
	}
	if size := v.MetadataCache.MaxSize; size < 1 {
		errs = append(errs, fmt.Errorf(
			"%w: virtual.metadata_cache.max_size %s must be more than zero", ErrInvalidValue, size))
	}
	return errs
}

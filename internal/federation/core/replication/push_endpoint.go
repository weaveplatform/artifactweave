package replication

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PushEndpoint derives normal item and storage routes from the configured
// target repository URL. File names are data, including matrix separators;
// parsing them as URL references would permit origin or namespace changes.
type PushEndpoint struct {
	base       url.URL
	prefix     string
	repository string
}

// NewPushEndpoint validates a configured repository URL without network I/O.
func NewPushEndpoint(raw string) (PushEndpoint, error) {
	if err := validateURL(raw); err != nil {
		return PushEndpoint{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.ForceQuery || !utf8.ValidString(u.Path) {
		return PushEndpoint{}, fmt.Errorf("invalid replication target: %w", kernel.ErrInvalid)
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" || !strings.HasPrefix(path, "/") || !canonicalPushPath(strings.TrimPrefix(path, "/")) {
		return PushEndpoint{}, fmt.Errorf("replication target must name a repository: %w", kernel.ErrInvalid)
	}
	last := strings.LastIndexByte(path, '/')
	return PushEndpoint{base: *u, prefix: path[:last], repository: path[last+1:]}, nil
}

func canonicalPushPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || path != strings.Trim(path, "/") {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil
}

// ItemURL addresses normal item admission, never a physical blob.
func (e PushEndpoint) ItemURL(path string) (*url.URL, error) {
	return e.route(e.prefix+"/"+e.repository, path)
}

// StorageURL addresses the exact item metadata resource.
func (e PushEndpoint) StorageURL(path string) (*url.URL, error) {
	return e.route(e.prefix+"/api/storage/"+e.repository, path)
}

// MetadataURL addresses the exact item property resource.
func (e PushEndpoint) MetadataURL(path string) (*url.URL, error) {
	return e.route(e.prefix+"/api/metadata/"+e.repository, path)
}

func (e PushEndpoint) route(base, path string) (*url.URL, error) {
	if e.repository == "" || !canonicalPushPath(path) {
		return nil, fmt.Errorf("invalid replication item path: %w", kernel.ErrInvalid)
	}
	u := e.base
	u.Path = base + "/" + path
	parts := strings.Split(u.Path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	u.RawPath = strings.Join(parts, "/")
	u.RawQuery, u.Fragment = "", ""
	return &u, nil
}

// Repository is the configured destination repository key.
func (e PushEndpoint) Repository() string { return e.repository }

// AQLURL addresses the deployment's authenticated search API.
func (e PushEndpoint) AQLURL() (*url.URL, error) {
	return e.route(e.prefix+"/api/search", "aql")
}

// StorageRootURL permits the repository root for a nonmutating file listing.
func (e PushEndpoint) StorageRootURL(root string) (*url.URL, error) {
	if root != "" {
		return e.StorageURL(root)
	}
	return e.route(e.prefix+"/api/storage", e.repository)
}

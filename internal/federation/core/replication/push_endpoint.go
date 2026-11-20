package replication

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// pushEndpoint derives normal item and storage routes from the configured
// target repository URL. File names are data, including matrix separators;
// parsing them as URL references would permit origin or namespace changes.
type pushEndpoint struct {
	base       url.URL
	prefix     string
	repository string
}

func newPushEndpoint(raw string) (pushEndpoint, error) {
	if err := validateURL(raw); err != nil {
		return pushEndpoint{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.ForceQuery || !utf8.ValidString(u.Path) {
		return pushEndpoint{}, fmt.Errorf("invalid replication target: %w", kernel.ErrInvalid)
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" || !strings.HasPrefix(path, "/") || !canonicalPushPath(strings.TrimPrefix(path, "/")) {
		return pushEndpoint{}, fmt.Errorf("replication target must name a repository: %w", kernel.ErrInvalid)
	}
	last := strings.LastIndexByte(path, '/')
	return pushEndpoint{base: *u, prefix: path[:last], repository: path[last+1:]}, nil
}

func canonicalPushPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || path != strings.Trim(path, "/") {
		return false
	}
	_, _, _, err := kernel.SplitItemPath(path)
	return err == nil
}

func (e pushEndpoint) itemURL(path string) (*url.URL, error) {
	return e.route(e.prefix+"/"+e.repository, path)
}

func (e pushEndpoint) storageURL(path string) (*url.URL, error) {
	return e.route(e.prefix+"/api/storage/"+e.repository, path)
}

func (e pushEndpoint) route(base, path string) (*url.URL, error) {
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

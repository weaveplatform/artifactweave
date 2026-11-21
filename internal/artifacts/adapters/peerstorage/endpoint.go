package peerstorage

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Peer names and file-list names are decoded data. Escape each segment once so
// a literal matrix/query/fragment/percent character cannot become URL syntax.
// The peer parser fixes the authority/repository; only relative names vary.
func peerStorageURL(peer remote.SmartPeer, path string, query url.Values) (string, error) {
	if !utf8.ValidString(path) || path != strings.Trim(path, "/") ||
		strings.ContainsFunc(path, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", source.ErrInvalidRequest
	}
	if path != "" {
		if _, _, _, err := kernel.SplitItemPath(path); err != nil {
			return "", source.ErrInvalidRequest
		}
	}
	base, err := remote.CheckedURL(peer.BaseURL)
	if err != nil || peer.Repository == "" || strings.ContainsAny(peer.Repository, "/\\\r\n?#%:;") {
		return "", source.ErrInvalidRequest
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/storage/" + peer.Repository
	if path != "" {
		base.Path += "/" + path
	}
	parts := strings.Split(base.Path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	base.RawPath = strings.Join(parts, "/")
	base.RawQuery, base.Fragment, base.RawFragment, base.ForceQuery = query.Encode(), "", "", false
	return base.String(), nil
}

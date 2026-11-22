package peerstorage

import (
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

func TestUnitPullStorageAddressRefusesMalformedBoundNames(t *testing.T) {
	for _, tc := range []struct{ name, base, repository, path string }{
		{"control path", "https://peer.example/artifactory", "source", "bad\x00path"},
		{"invalid UTF8", "https://peer.example/artifactory", "source", string([]byte{0xff})},
		{"invalid authority", "file:///tmp/peer", "source", "a"},
		{"repository escape", "https://peer.example/artifactory", "other/repo", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, err := peerStorageURL(remote.SmartPeer{BaseURL: tc.base, Repository: tc.repository}, tc.path, nil)
			if !errors.Is(err, source.ErrInvalidRequest) || url != "" {
				t.Fatal("invalid stored peer names constructed a request endpoint", url, err)
			}
		})
	}
}

package vcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

// RefEndpoint binds a provider's discovery URL to its metadata parser.
type RefEndpoint struct {
	URL      *url.URL
	peer     bool
	branches bool
}

// RefEndpoints uses Git discovery for Git providers and the publisher's
// documented tag/branch JSON schema for an Artifactory peer.
func RefEndpoints(c repo.Config, owner, name string) ([]RefEndpoint, error) {
	if c.VCSGitProvider != repo.ArtifactoryVCS {
		u, err := GitRefsURL(c, owner, name)
		if err != nil {
			return nil, err
		}
		return []RefEndpoint{{URL: u}}, nil
	}
	endpoints := make([]RefEndpoint, 0, 2)
	for _, action := range []string{"tags", "branches"} {
		u, err := peerURL(c.URL, owner, name, action)
		if err != nil {
			return nil, err
		}
		endpoints = append(
			endpoints,
			RefEndpoint{URL: u, peer: true, branches: action == "branches"},
		)
	}
	return endpoints, nil
}

func (e RefEndpoint) Variant() string {
	if !e.peer {
		return "vcs-git-refs-v1"
	}
	if e.branches {
		return "vcs-peer-branches-v1"
	}
	return "vcs-peer-tags-v1"
}

func (e RefEndpoint) Parse(data []byte) ([]Ref, error) {
	if !e.peer {
		return ParseRefs(data)
	}
	return parsePeerRefs(data, e.branches)
}

func peerURL(raw, owner, name, action string) (*url.URL, error) {
	u, err := providerBase(raw)
	if err != nil || !ValidProject(owner, name) || strings.Contains(owner, "/") {
		return nil, ErrInvalidPath
	}
	context, key, ok := strings.Cut(strings.TrimSuffix(u.Path, "/"), "/api/vcs/")
	if !ok || !validProjectSegment(key) {
		return nil, ErrInvalidPath
	}
	u.Path = context + "/api/vcs/" + action + "/" + key + "/" + owner + "/" + name
	return u, nil
}

// PeerFileURL addresses the peer's selected-file API. Its reference is mutable;
// callers must not cache the response as an immutable commit archive.
func PeerFileURL(c repo.Config, owner, name, kind, ref, filename string) (*url.URL, error) {
	if !ValidRef(ref) || !ValidFilePath(filename) || kind != "tags" && kind != "heads" {
		return nil, ErrInvalidPath
	}
	action := "downloadTagFile"
	if kind == "heads" {
		action = "downloadBranchFile"
	}
	u, err := peerURL(c.URL, owner, name, action)
	if err != nil {
		return nil, err
	}
	u.Path += "/" + ref + "!" + filename
	u.RawQuery = "ext=zip"
	return u, nil
}

func parsePeerRefs(data []byte, branches bool) ([]Ref, error) {
	if len(data) == 0 || len(data) > MaxRefsBytes ||
		!bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return nil, ErrInvalidRefs
	}
	//nolint:tagliatelle // Mirrors the publisher's VCS peer JSON schema.
	type peerRef struct {
		Name     string `json:"name"`
		CommitID string `json:"commitId"` //nolint:tagliatelle // JFrog VCS wire field.
		Branch   *bool  `json:"isBranch"` //nolint:tagliatelle // JFrog VCS wire field.
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := decoder.Token(); err != nil {
		return nil, ErrInvalidRefs
	}
	refs := make([]Ref, 0)
	seen := make(map[string]bool)
	for decoder.More() {
		if len(refs) >= MaxRefs {
			return nil, ErrInvalidRefs
		}
		var row peerRef
		if err := decoder.Decode(&row); err != nil {
			return nil, ErrInvalidRefs
		}
		if !ValidRef(row.Name) || !ValidCommitID(row.CommitID) || row.Branch == nil ||
			*row.Branch != branches || seen[row.Name] {
			return nil, ErrInvalidRefs
		}
		seen[row.Name] = true
		refs = append(refs, Ref{Name: row.Name, CommitID: row.CommitID, Branch: branches})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, ErrInvalidRefs
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRefs
	}
	return refs, nil
}

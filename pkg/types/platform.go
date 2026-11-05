package types

import (
	"path"
	"slices"
	"time"
)

// FederatedEntity is an entity type Access Federation streams.
type FederatedEntity string

// Federated entity types.
const (
	EntityUsers       FederatedEntity = "USERS"
	EntityGroups      FederatedEntity = "GROUPS"
	EntityPermissions FederatedEntity = "PERMISSIONS"
	EntityTokens      FederatedEntity = "TOKENS"
)

// PatternFilter includes and excludes names by glob; an empty include list
// includes everything.
type PatternFilter struct {
	Include []string `json:"include_patterns,omitempty"`
	Exclude []string `json:"exclude_patterns,omitempty"`
}

// Matches reports whether name passes the filter.
func (f PatternFilter) Matches(name string) bool {
	match := func(p string) bool { ok, err := path.Match(p, name); return err == nil && ok }
	if len(f.Include) > 0 && !slices.ContainsFunc(f.Include, match) {
		return false
	}
	return !slices.ContainsFunc(f.Exclude, match)
}

// AccessFederationTarget is a peer that receives this instance's identity
// changes on the access stream (decision 0025).
type AccessFederationTarget struct {
	Name              string
	PeerInstanceID    string
	Active            bool
	Entities          []FederatedEntity
	PermissionFilters PatternFilter
	GroupFilters      PatternFilter
	Created           time.Time
}

// Streams reports whether the target receives entity e.
func (t AccessFederationTarget) Streams(e FederatedEntity) bool {
	return t.Active && slices.Contains(t.Entities, e)
}

// PlatformFederation is a named set of connection groups of follower JPDs.
// The entity types are Curation's and carry no data in artifactweave; they
// are stored so the federation round-trips.
type PlatformFederation struct {
	ID               string
	Name             string
	Description      string
	ConnectionGroups map[string][]string
	Entities         []string
}

// TopologySite is one site of a topology, backed by a peer.
type TopologySite struct {
	PeerInstanceID string
	SiteName       string
}

// Topology is a named arrangement of sites.
type Topology struct {
	Key         string
	Name        string
	Description string
	Sites       []TopologySite
	Created     time.Time
	Modified    time.Time
}

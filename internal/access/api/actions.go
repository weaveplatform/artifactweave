package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnknownAction reports an action name or letter no wire form defines.
var ErrUnknownAction = errors.New("unknown permission action")

// ResourceType is a section of an Access v2 permission.
type ResourceType string

// Resource types. Destination carries Distribution's DESTINATION permissions
// (decision 0023); pipeline sources are accepted for round-tripping only.
const (
	ResourceArtifact       ResourceType = "artifact"
	ResourceBuild          ResourceType = "build"
	ResourceReleaseBundle  ResourceType = "release_bundle"
	ResourceDestination    ResourceType = "destination"
	ResourcePipelineSource ResourceType = "pipeline_source"
)

// Special targets. A target is otherwise a repository key (or, for builds,
// the build-info repository).
const (
	TargetAny             = "ANY"
	TargetAnyLocal        = "ANY LOCAL"
	TargetAnyRemote       = "ANY REMOTE"
	TargetAnyDistribution = "ANY DISTRIBUTION"
	// BuildInfoRepository is the fixed target of the legacy build section.
	BuildInfoRepository = "artifactory-build-info"
	// ReleaseBundlesRepository is the default target of the legacy release
	// bundle section, the default release bundle source repository.
	ReleaseBundlesRepository = "release-bundles"
)

// Actions is a set of permission actions. The internal model uses the Access
// v2 vocabulary; the legacy letters and names map onto it one to one
// (managedXrayMeta is SCAN, distribute is EXECUTE).
type Actions uint8

// Actions, in the order the Access API lists them.
const (
	ActionRead Actions = 1 << iota
	ActionAnnotate
	ActionWrite
	ActionDelete
	ActionManage
	ActionScan
	ActionExecute
)

type actionNames struct {
	action Actions
	access string
	legacy string
	letter string
}

var actionTable = []actionNames{
	{ActionRead, "READ", "read", "r"},
	{ActionAnnotate, "ANNOTATE", "annotate", "n"},
	{ActionWrite, "WRITE", "write", "w"},
	{ActionDelete, "DELETE", "delete", "d"},
	{ActionManage, "MANAGE", "manage", "m"},
	{ActionScan, "SCAN", "managedXrayMeta", "mxm"},
	{ActionExecute, "EXECUTE", "distribute", "x"},
}

// Has reports whether every action in want is in a.
func (a Actions) Has(want Actions) bool { return a&want == want }

func (a Actions) names(pick func(actionNames) string) []string {
	var out []string
	for _, n := range actionTable {
		if a.Has(n.action) {
			out = append(out, pick(n))
		}
	}
	return out
}

// AccessNames are the actions in the Access v2 spelling.
func (a Actions) AccessNames() []string {
	return a.names(func(n actionNames) string { return n.access })
}

// LegacyNames are the actions in the legacy v2 permission-target spelling.
func (a Actions) LegacyNames() []string {
	return a.names(func(n actionNames) string { return n.legacy })
}

// Letters are the actions in the legacy v1 permission-target spelling.
func (a Actions) Letters() []string { return a.names(func(n actionNames) string { return n.letter }) }

func parseActions(names []string, pick func(actionNames) string, fold bool) (Actions, error) {
	var a Actions
	for _, name := range names {
		found := false
		for _, n := range actionTable {
			if pick(n) == name || (fold && strings.EqualFold(pick(n), name)) {
				a |= n.action
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("%w: %q", ErrUnknownAction, name)
		}
	}
	return a, nil
}

// ParseAccessActions reads Access v2 action names, case-insensitively as
// Access does.
func ParseAccessActions(names []string) (Actions, error) {
	return parseActions(names, func(n actionNames) string { return n.access }, true)
}

// ParseLegacyActions reads legacy v2 action names.
func ParseLegacyActions(names []string) (Actions, error) {
	return parseActions(names, func(n actionNames) string { return n.legacy }, false)
}

// ParseLetters reads legacy v1 action letters.
func ParseLetters(letters []string) (Actions, error) {
	return parseActions(letters, func(n actionNames) string { return n.letter }, false)
}

// MarshalJSON stores actions as Access names so a permission document reads
// the same as the Access API.
func (a Actions) MarshalJSON() ([]byte, error) {
	names := a.AccessNames()
	if names == nil {
		names = []string{}
	}
	b, err := json.Marshal(names)
	if err != nil {
		return nil, fmt.Errorf("marshal actions: %w", err)
	}
	return b, nil
}

// UnmarshalJSON reads Access names.
func (a *Actions) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return fmt.Errorf("unmarshal actions: %w", err)
	}
	v, err := ParseAccessActions(names)
	if err != nil {
		return err
	}
	*a = v
	return nil
}

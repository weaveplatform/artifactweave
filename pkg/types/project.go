package types

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidProjectKey reports a project key outside the documented pattern.
var ErrInvalidProjectKey = errors.New("invalid project key")

var projectKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// ValidateProjectKey checks the documented key rule: 2 to 32 characters,
// lower-case letters, digits and hyphens, starting with a letter. The key
// prefixes repository keys, so it is immutable once created.
func ValidateProjectKey(key string) error {
	if !projectKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q", ErrInvalidProjectKey, key)
	}
	return nil
}

// ProjectRepoKey is the key a repository created inside a project gets.
func ProjectRepoKey(projectKey, name string) string {
	prefix := projectKey + "-"
	if strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

// AdminPrivileges are the project admin switches, stored as one document.
type AdminPrivileges struct {
	ManageMembers           bool `json:"manage_members,omitempty"`
	ManageResources         bool `json:"manage_resources,omitempty"`
	IndexResources          bool `json:"index_resources,omitempty"`
	ManageRemoteRepository  bool `json:"manage_remote_repository,omitempty"`
	CreateRemoteRepository  bool `json:"create_remote_repository,omitempty"`
	DeleteRemoteRepository  bool `json:"delete_remote_repository,omitempty"`
	CreateLocalRepository   bool `json:"create_local_repository,omitempty"`
	DeleteLocalRepository   bool `json:"delete_local_repository,omitempty"`
	CreateVirtualRepository bool `json:"create_virtual_repository,omitempty"`
	DeleteVirtualRepository bool `json:"delete_virtual_repository,omitempty"`
}

// Project groups repositories, members and roles under a key.
type Project struct {
	Key               string
	DisplayName       string
	Description       string
	AdminPrivileges   AdminPrivileges
	StorageQuotaBytes int64 // -1 or 0 means unlimited, as the provider sends
	SoftLimit         bool
	QuotaEmail        bool
}

// RoleType distinguishes built-in from administrator-defined roles.
type RoleType string

// Role types.
const (
	RoleAdmin        RoleType = "ADMIN"
	RolePredefined   RoleType = "PREDEFINED"
	RoleCustom       RoleType = "CUSTOM"
	RoleCustomGlobal RoleType = "CUSTOM_GLOBAL"
)

// Role is a named set of actions over environments. ProjectKey is empty for
// global roles.
type Role struct {
	ProjectKey   string
	Name         string
	Type         RoleType
	Description  string
	Environments []string
	Actions      []string
}

// Editable reports whether administrators may change every field of the
// role, name and description included. A PREDEFINED global role's actions and
// environments can still be changed (roles.Service.UpdateRole); an ADMIN
// role's cannot.
func (r Role) Editable() bool { return r.Type == RoleCustom || r.Type == RoleCustomGlobal }

// Built-in environments.
const (
	EnvironmentDev  = "DEV"
	EnvironmentProd = "PROD"
)

// Environment is a deployment stage repositories are assigned to. ProjectKey
// is empty for global environments.
type Environment struct {
	ProjectKey string
	Name       string
}

// PrincipalType distinguishes users from groups in memberships and grants.
type PrincipalType string

// Principal types.
const (
	PrincipalUser  PrincipalType = "user"
	PrincipalGroup PrincipalType = "group"
)

// ProjectMember is a user or group's roles in one project.
type ProjectMember struct {
	ProjectKey string
	Type       PrincipalType
	Name       string
	Roles      []string
}

// RepositoryAssignment records which project owns a repository and with
// whom it is shared.
type RepositoryAssignment struct {
	RepoKey        string
	ProjectKey     string
	Environments   []string
	SharedWith     []string
	SharedWithAll  bool
	SharedReadOnly bool
	Updated        time.Time
}

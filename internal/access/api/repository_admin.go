package api

// RepositoryOperation is a configuration mutation, separate from content
// Read/Write/Delete. Project management never manufactures content actions.
type RepositoryOperation uint8

const (
	// RepositoryCreate asks for the effective class-specific create privilege.
	RepositoryCreate RepositoryOperation = iota + 1
	// RepositoryUpdate asks for configuration management in the owning project.
	RepositoryUpdate
	// RepositoryDelete asks for the effective class-specific delete privilege.
	RepositoryDelete
)

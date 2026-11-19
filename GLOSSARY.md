# Artifact registry compatibility

Language used to describe the client contracts mirrored by ArtifactWeave.

## Language

**Error response profile**:
The error-response contract expected by a client API or protocol, covering its envelope, media type, and authentication challenge. JFrog Distribution and OCI registries have distinct error response profiles.
_Avoid_: API surface when referring specifically to this error contract

**Project-aware sharing**:
A repository sharing method in which another project reads through an internal remote while retaining the requesting user and receiving project's identity and permissions.
_Avoid_: Read-only sharing when naming the sharing method

**Share permission ceiling**:
The maximum access a receiving project may exercise through a repository share, intersected with the caller's own permissions. It is distinct from the sharing method.
_Avoid_: Sharing mode when referring to the share's read or write limits

# Artifact registry compatibility

Language used to describe the client contracts mirrored by ArtifactWeave.

## Language

**Error response profile**:
The error-response contract expected by a client API or protocol, covering its envelope, media type, and authentication challenge. JFrog Distribution and OCI registries have distinct error response profiles.
_Avoid_: API surface when referring specifically to this error contract

# Authored peer contract notes

These notes summarize the publisher schemas identified in [provenance.json](provenance.json).
Original proprietary definitions stay in the ignored local `.specoracle` cache.

| Operation | Successful response interpreted by the adapter |
| --- | --- |
| Get VCS Tags | JSON array. Each reference has a string `name`, string Git object ID `commitId`, and boolean `isBranch` identifying a tag. |
| Get VCS Branches | JSON array with the same fields, identifying branches. |
| Download a VCS Commit | Archive bytes for the requested repository and object ID; `ext` selects ZIP or gzip-compressed TAR. |

The adapter additionally enforces bounded payload/count limits, valid names and
object IDs, matching reference kinds, and unique names. These checks describe
artifactweave behavior, not additional guarantees inferred from the publisher.
The peer `refs` string's inner format is unspecified and is not consumed.

# JetBrains Plugins remote repositories

JetBrains Plugins uses the read-only **remote** class. This follows the
[JFrog repository guide](https://docs.jfrog.com/artifactory/docs/jetbrains-plugins-repositories)
and [current package-client class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients).
The IDE is configured as a replacement Marketplace host, rather than as a
custom `updatePlugins.xml` repository:

```properties
idea.plugins.host=https://registry.example/artifactory/api/jetbrainsplugins/jetbrains-remote
```

Create `jetbrains-remote` with `rclass=remote`, `packageType=jetbrainsplugins`
and the upstream Marketplace URL `https://plugins.jetbrains.com`. The IDE
does not send Artifactory credentials to this host. An administrator must
enable anonymous access and grant `anonymous` **READ** on this repository:

```http
PATCH /artifactory/api/system/configuration
Authorization: Basic <admin credentials>
Content-Type: application/yaml

security:
  anonAccessEnabled: true
```

```http
POST /access/api/v2/permissions
Authorization: Basic <admin credentials>
Content-Type: application/json

{"name":"jetbrains-read","resources":{"artifact":{"actions":{"users":{"anonymous":["READ"]}},"targets":{"jetbrains-remote":{"include_patterns":["**"]}}}}}
```

The instance switch alone grants no repository access. The configuration
descriptor accepts only `security.anonAccessEnabled`; other security keys
return an error.

## IDE protocol

IDEA Community **2025.2.5** (`idea.sh installPlugins com.weave.fixture`)
made these requests against a controlled Marketplace host. The
[first-party Marketplace URL definitions](https://github.com/JetBrains/intellij-community/blob/7309c32e34570863a2be7eadca4ab58090267eaf/platform/platform-impl/src/com/intellij/ide/plugins/marketplace/utils/MarketplaceUrls.kt)
also describe catalog and plugin ID-list routes.

| Operation | IDE request |
| --- | --- |
| Compatible update | `GET /api/search/updates/compatible?build=IC-252.28238.7&os=Linux+6.12.76-linuxkit&pluginXmlId=com.weave.fixture` |
| Update metadata | `GET /files/98765/12345/meta.json` |
| Archive resolution | `GET /pluginManager?id=com.weave.fixture&build=IC-252.28238.7&uuid=…&updatedFrom` |
| Archive redirect | `GET /files/98765/12345/<name>.zip` or `<name>.jar` |
| Catalog search | `GET /api/search/plugins?build=<build>&search=<term>` |

The adapter preserves upstream numeric plugin/update IDs and versions, validates
metadata and the archive's `META-INF/plugin.xml` identity, and normalizes the
request UUID out of cache identity. Marketplace plugin IDs are opaque: the
official `pluginsXMLIds.json` catalog includes spaces and `/` in legacy IDs.
Such IDs use deterministic hashed internal cache paths and sanitized download
filenames; their original values remain in protocol metadata and upstream
queries. Every archive entry is streamed under an expanded-size bound so
CRC failures and unsafe paths cannot enter the cache. Redirects are limited to
validated file
paths on the configured origin, plus
`downloads.marketplace.jetbrains.com` for an official
`plugins.jetbrains.com` source. The download filename is derived from the
validated plugin ID and archive extension. The remote source service owns
cache refresh, offline reads and checksums. Deleting a cached representation
invalidates it; the next request refetches it. Remote cache content is not
trashed for restore. The catalog is upstream-authored, so there is no derived
JetBrains index to rebuild.

The [native acceptance scenario](../../test/acceptance/features/jetbrainsplugins.feature)
uses a pinned Linux x86-64 IDE archive, a controlled upstream and a fresh IDE
profile for both the cold install and an offline cache install. The
[client Dockerfile](../../test/acceptance/clients/jetbrains.Dockerfile)
verifies the official SHA-256 before extraction. The ARM64 archive was also
verified locally and the real CLI install passed against the adapter before
the large temporary IDE extraction was removed.

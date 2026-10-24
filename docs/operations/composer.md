# PHP Composer repositories

Checked against the [JFrog PHP Composer guide](https://docs.jfrog.com/artifactory/docs/php-composer-repositories) and the [Composer repository specification](https://getcomposer.org/doc/05-repositories.md) on 2026-10-24. Acceptance pins Composer 2.10.3 and 1.10.28.

Create a repository with `packageType: composer`. Local and one-node federated repositories accept ZIP archives containing `composer.json` at the root or in one top-level directory. Its lowercase `vendor/package` name must match the first two path segments. Deploy to `/artifactory/<repo>/vendor/package/<file>.zip`. The embedded version is required, or supply exactly one `;composer.version=<version>` matrix parameter. The embedded and matrix versions must agree when both are present. An invalid, oversized, or corrupt archive is refused before publication.

Configure a project with the repository URL including `/api/composer`:

```json
{
  "repositories": [
    {"type": "composer", "url": "https://registry.example/artifactory/api/composer/php-virtual"},
    {"packagist.org": false}
  ],
  "require": {"acme/widget": "^1.0"}
}
```

Run `composer install --prefer-dist`. For an authenticated repository, configure Composer's `auth.json` `http-basic` entry for the registry host, including its port if present. Composer 2 reads `packages.json` and its advertised `metadata-url`, then `/p2/vendor/package.json`; development versions use `/p2/vendor/package~dev.json`. Unknown packages return 404. Local repositories return a full version map in `packages.json` to Composer 1 user agents through the JFrog support window ending December 2026. Composer 2 is required for remote and virtual repositories.

The metadata's `dist.url` points back through the same Composer repository, preserving virtual member order and caller permissions. Hosted downloads carry the archive's SHA-1 `shasum`; remote downloads validate upstream metadata, the declared SHA-1 when present, and the ZIP's package identity. Remote metadata and artifacts use the shared source cache and transport policy. For an offline read of already cached content, use the repository's `-cache` alias in the Composer URL. A cache miss stays a miss and does not contact the upstream.

`POST /artifactory/api/composer/<repo>/reindex` requires Manage on a local or federated repository and verifies every stored archive. The metadata view is derived from current items, so deletion and restoration appear immediately without waiting for an index job. A virtual repository selects the first readable member that contains a package; order follows its `repositories` setting. Cross-peer federation convergence belongs to P13.

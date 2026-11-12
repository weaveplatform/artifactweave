Feature: npm audit signatures verifies what an npm local repository signs
  7.x signs an npm local repository's packages with the ECDSA key pair its
  configuration names, and saving that key reindexes the repository and
  signs every package (decision 0034, "Signatures"). npm audit signatures
  verifies each installed version's dist.signatures with the keys the
  registry lists at /-/npm/v1/keys. Before it asks the registry, npm
  initialises the Sigstore TUF client, which fetches its metadata from
  tuf-repo-cdn.sigstore.dev: the scenario needs that host, and fails
  without it before npm reaches the registry (docs/testing/npm.md).

  Background:
    Given the registry is running

  @npm
  Scenario Outline: <npm> verifies the signatures of an npm local repository, and again after its key pair rotates
    Given the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-signed" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-signed/
      //{host}/artifactory/api/npm/npm-signed/:_auth={basicauth}
      """
    And a file "acme-early/package.json" with content:
      """
      {"name": "acme-early", "version": "1.0.0", "main": "index.js"}
      """
    And a file "acme-early/index.js" with content:
      """
      module.exports = 'published before the repository signed';
      """
    And a file "acme-late/package.json" with content:
      """
      {"name": "acme-late", "version": "1.0.0", "main": "index.js"}
      """
    And a file "acme-late/index.js" with content:
      """
      module.exports = 'published after';
      """
    And a file "consumer/package.json" with content:
      """
      {"name": "consumer", "version": "1.0.0", "private": true}
      """
    When I run npm "publish ./acme-early"
    Then the command succeeds
    When I run npm "install --prefix consumer acme-early"
    Then the command succeeds
    # With no key pair named, the registry lists no keys and npm has none
    # to audit with, as before signing existed.
    When I run npm "audit signatures --prefix consumer"
    Then the command fails
    And the output contains "found no dependencies to audit that were installed from a supported registry"

    # Naming a pair queues the reindex that signs what was stored before.
    When I create the ECDSA key pair "npm-sign-a" with alias "npm-a" as admin
    Then the response status is 201
    When I POST "/artifactory/api/repositories/npm-signed" as admin with body:
      """
      {"primaryKeyPairRef":"npm-sign-a"}
      """
    Then the response status is 200
    And the queued reindex of "npm-signed" completes within 30 seconds
    When I run npm "publish ./acme-late"
    Then the command succeeds
    When I run npm "install --prefix consumer acme-late"
    Then the command succeeds
    When I run npm "audit signatures --prefix consumer"
    Then the command succeeds
    And the output contains "2 packages have verified registry signatures"

    # A new pair, from gpg's armored export as 7.x's documentation makes
    # one, signs every version again: npm-sign-a is no longer listed, so a
    # version still signed with it would fail the audit.
    When I create the ECDSA key pair "npm-sign-b" with alias "npm-b" from a gpg export as admin
    Then the response status is 201
    When I POST "/artifactory/api/repositories/npm-signed" as admin with body:
      """
      {"primaryKeyPairRef":"npm-sign-b"}
      """
    Then the response status is 200
    And the queued reindex of "npm-signed" completes within 30 seconds
    When I run npm "audit signatures --prefix consumer"
    Then the command succeeds
    And the output contains "2 packages have verified registry signatures"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

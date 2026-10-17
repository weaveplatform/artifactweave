Feature: npm reads from npm local repositories
  The registry at /artifactory/api/npm/<repo>/ builds packuments from the
  package index the npm format keeps (decision 0034), so a tarball
  deployed to its canonical path by REST, as jf npm publish deploys it,
  installs with npm. npm runs in the container pinned by digest
  (test/acceptance/clients).

  Background:
    Given the registry is running

  @npm
  Scenario: npm installs a package deployed by REST to its canonical path
    Given the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-read" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    # Only the tarball is deployed; the packument the step also writes is
    # left unused, since the registry builds its own.
    Given an npm package "acme-read" at "1.0.0" with its tarball at "/artifactory/api/npm/npm-read/acme-read/-/acme-read-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-read-1.0.0.tgz" to "/artifactory/npm-read/acme-read/-/acme-read-1.0.0.tgz" as admin
    Then the response status is 201
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-read/
      //{host}/artifactory/api/npm/npm-read/:_auth={basicauth}
      """
    When I run npm "ping"
    Then the command succeeds
    When I run npm "view acme-read dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0"
    When I run npm "install acme-read"
    Then the command succeeds
    And the file "node_modules/acme-read/package.json" contains "1.0.0"
    And the file "package-lock.json" contains "/artifactory/api/npm/npm-read/acme-read/-/acme-read-1.0.0.tgz"
    And the file "package-lock.json" contains "sha512-"

Feature: npm reads from and publishes to npm local repositories
  The registry at /artifactory/api/npm/<repo>/ builds packuments from the
  package index the npm format keeps (decision 0034), so a tarball
  deployed to its canonical path by REST, as jf npm publish deploys it,
  installs with npm, and so does one npm publish stores. npm runs in the
  container pinned by digest (test/acceptance/clients).

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

  @npm
  Scenario: npm publishes unscoped and scoped packages, and a version only once
    Given the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-publish" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-publish/
      //{host}/artifactory/api/npm/npm-publish/:_auth={basicauth}
      """
    And a file "acme-pub/package.json" with content:
      """
      {"name": "acme-pub", "version": "1.0.0", "main": "index.js"}
      """
    And a file "acme-pub/index.js" with content:
      """
      module.exports = 'published by npm';
      """
    And a file "scoped-pub/package.json" with content:
      """
      {"name": "@acme/scoped-pub", "version": "2.0.0", "main": "index.js"}
      """
    And a file "scoped-pub/index.js" with content:
      """
      module.exports = 'scoped';
      """
    When I run npm "publish ./acme-pub"
    Then the command succeeds
    # 201, as 7.x answers a publish.
    And the server answered 201 to PUT "/artifactory/api/npm/npm-publish/acme-pub"
    When I run npm "publish ./scoped-pub --tag next"
    Then the command succeeds
    When I run npm "view acme-pub dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0"
    When I run npm "view @acme/scoped-pub dist-tags.next"
    Then the command succeeds
    And the output contains "2.0.0"
    When I run npm "install acme-pub @acme/scoped-pub@2.0.0"
    Then the command succeeds
    And the file "node_modules/acme-pub/index.js" contains "published by npm"
    And the file "node_modules/@acme/scoped-pub/package.json" contains "2.0.0"
    # npm checks for the version itself unless --force is given; the
    # registry refuses it either way.
    When I run npm "publish ./acme-pub --force"
    Then the command fails
    And the output contains "403"

  @npm
  Scenario: npm publish meets the release and snapshot handling, and npm revalidates packuments
    Given the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-releases" as admin with body:
      """
      {"rclass":"local","packageType":"npm","handleSnapshots":false}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-releases/
      //{host}/artifactory/api/npm/npm-releases/:_auth={basicauth}
      """
    And a file "rel/package.json" with content:
      """
      {"name": "acme-rel", "version": "1.0.0", "main": "index.js", "license": "Python-2.0"}
      """
    And a file "rel/index.js" with content:
      """
      module.exports = 'release';
      """
    And a file "pre/package.json" with content:
      """
      {"name": "acme-rel", "version": "1.1.0-beta.1", "main": "index.js"}
      """
    And a file "pre/index.js" with content:
      """
      module.exports = 'pre-release';
      """
    When I run npm "publish ./rel"
    Then the command succeeds
    # A pre-release is a snapshot, which npm-releases does not handle.
    When I run npm "publish ./pre --tag beta"
    Then the command fails
    And the output contains "409"
    And the output contains "snapshot release handling policy"
    # npm view asks again each time, with the ETag it was given.
    When I run npm "view acme-rel dist-tags.latest"
    Then the command succeeds
    When I run npm "view acme-rel dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0"
    And the server answered 304 to GET "/artifactory/api/npm/npm-releases/acme-rel"
    # Indexing writes package.json's license, as 7.x does.
    When I GET "/artifactory/api/storage/npm-releases/acme-rel/-/acme-rel-1.0.0.tgz?properties" as admin
    Then the response status is 200
    And the response body contains "artifactory.licenses"
    And the response body contains "Python-2.0"

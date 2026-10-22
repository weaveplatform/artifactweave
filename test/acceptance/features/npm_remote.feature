@npm @npm_remote
Feature: npm remote metadata and immutable tarballs
  Raw upstream metadata is retained separately from request-specific URLs.
  This fixture proves our implementation; vendor parity awaits P19 captures.

  Scenario Outline: <npm> installs cold scoped and unscoped packages and repeats from the offline server cache
    Given the registry is running
    And the <npm> client can reach the registry
    And an npm upstream serves "remote-demo" at "1.0.0"
    And an npm upstream serves "@acme/remote-scoped" at "1.0.0"
    When I PUT "/artifactory/api/repositories/npm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"npm","url":"{upstream}"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-proxy/
      //{host}/artifactory/api/npm/npm-proxy/:_auth={basicauth}
      """
    And a file "package.json" with content:
      """
      {"name":"remote-consumer","version":"1.0.0","dependencies":{"remote-demo":"1.0.0","@acme/remote-scoped":"1.0.0"}}
      """
    When I run npm "install --ignore-scripts --cache .npm-cold"
    Then the command succeeds
    And the file "node_modules/remote-demo/package.json" contains "1.0.0"
    And the file "node_modules/@acme/remote-scoped/package.json" contains "1.0.0"
    And the lockfile pins "remote-demo" to the tarball at "/artifactory/api/npm/npm-proxy/remote-demo/-/remote-demo-1.0.0.tgz"
    And the lockfile pins "@acme/remote-scoped" to the tarball at "/artifactory/api/npm/npm-proxy/@acme/remote-scoped/-/@acme/remote-scoped-1.0.0.tgz"
    And the generic upstream received 1 GET requests for "/remote-demo/-/remote-demo-1.0.0.tgz"
    And the generic upstream received 1 GET requests for "/@acme/remote-scoped/-/acme/remote-scoped-1.0.0.tgz"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/npm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"npm","url":"{upstream}","offline":true}
      """
    Then the response status is 200
    When I run npm "ci --ignore-scripts --cache .npm-second-empty-cache"
    Then the command succeeds
    And the file "node_modules/remote-demo/package.json" contains "1.0.0"
    And the file "node_modules/@acme/remote-scoped/package.json" contains "1.0.0"
    And the generic upstream received 1 GET requests for "/remote-demo/-/remote-demo-1.0.0.tgz"
    And the generic upstream received 1 GET requests for "/@acme/remote-scoped/-/acme/remote-scoped-1.0.0.tgz"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

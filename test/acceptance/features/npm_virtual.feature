@npm @npm_virtual
Feature: npm virtual packuments and immutable dependency tarballs
  Local and remote packages resolve through one virtual registry URL.

  Scenario Outline: <npm> installs a cold mixed dependency tree and verifies its virtual lockfile with npm ci
    Given the registry is running
    And the <npm> client can reach the registry
    And an npm upstream serves "remote-leaf" at "1.0.0"
    And an npm upstream serves "@acme/remote-leaf" at "1.0.0"
    And an npm upstream serves "local-root" at "1.0.0"
    When I PUT "/artifactory/api/repositories/npm-local" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/npm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"npm","url":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/npm-all" as admin with body:
      """
      {"rclass":"virtual","packageType":"npm","repositories":["npm-local","npm-proxy"]}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-local/
      //{host}/artifactory/api/npm/npm-local/:_auth={basicauth}
      """
    And a file "hosted/package.json" with content:
      """
      {"name":"local-root","version":"1.0.0","description":"local winner","dependencies":{"@acme/remote-leaf":"1.0.0"}}
      """
    And a file "scoped/package.json" with content:
      """
      {"name":"@acme/local-root","version":"1.0.0","dependencies":{"remote-leaf":"1.0.0"}}
      """
    When I run npm "publish ./hosted --ignore-scripts"
    Then the command succeeds
    When I run npm "publish ./scoped --ignore-scripts"
    Then the command succeeds
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-all/
      //{host}/artifactory/api/npm/npm-all/:_auth={basicauth}
      """
    And a file "package.json" with content:
      """
      {"name":"virtual-consumer","version":"1.0.0","dependencies":{"local-root":"1.0.0","@acme/local-root":"1.0.0"}}
      """
    When I run npm "install --ignore-scripts --cache .npm-cold"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "node_modules/local-root/package.json" contains "local winner"
    And the file "node_modules/@acme/local-root/package.json" contains "remote-leaf"
    And the file "node_modules/remote-leaf/package.json" contains "1.0.0"
    And the file "node_modules/@acme/remote-leaf/package.json" contains "1.0.0"
    And the lockfile pins "local-root" to the tarball at "/artifactory/api/npm/npm-all/local-root/-/local-root-1.0.0.tgz"
    And the lockfile pins "@acme/local-root" to the tarball at "/artifactory/api/npm/npm-all/@acme/local-root/-/@acme/local-root-1.0.0.tgz"
    And the lockfile pins "remote-leaf" to the tarball at "/artifactory/api/npm/npm-all/remote-leaf/-/remote-leaf-1.0.0.tgz"
    And the lockfile pins "@acme/remote-leaf" to the tarball at "/artifactory/api/npm/npm-all/@acme/remote-leaf/-/@acme/remote-leaf-1.0.0.tgz"
    And the generic upstream received 0 GET requests for "/local-root/-/local-root-1.0.0.tgz"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/npm-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"npm","url":"{upstream}","offline":true}
      """
    Then the response status is 200
    When I run npm "ci --ignore-scripts --cache .npm-second-empty-cache"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file "node_modules/local-root/package.json" contains "local winner"
    And the file "node_modules/remote-leaf/package.json" contains "1.0.0"
    And the file "node_modules/@acme/remote-leaf/package.json" contains "1.0.0"
    And the generic upstream received 1 GET requests for "/remote-leaf/-/remote-leaf-1.0.0.tgz"
    And the generic upstream received 1 GET requests for "/@acme/remote-leaf/-/acme/remote-leaf-1.0.0.tgz"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

Feature: npm overwrites, latest by publish, and enforced paths
  An overwrite with other content leaves the file none of its properties,
  so a dropped version's dist-tags and deprecation go with it and the
  strict-layout conversion hands none of them on (#515, #518);
  npm.tag_latest_by_publish gives latest to the version published last, as
  7.x's artifactory.npm.tag.tagLatestByPublish does; and npm.enforce_path
  refuses a tarball uploaded under a file name its package.json does not
  give, as 7.x's artifactory.npm.enforce.path does (decision 0034). npm runs
  in the containers pinned by digest (test/acceptance/clients): each client
  scenario runs with npm 11, Node 24 LTS's own, and again with npm 12.

  @npm
  Scenario Outline: <npm> sees an overwrite with another version's tarball take its dist-tags and deprecation
    Given the registry is running
    And the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-over" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-over/
      //{host}/artifactory/api/npm/npm-over/:_auth={basicauth}
      """
    And a file "v1/package.json" with content:
      """
      {"name": "acme-over", "version": "1.0.0", "main": "index.js"}
      """
    And a file "v1/index.js" with content:
      """
      module.exports = 'one';
      """
    And a file "v2/package.json" with content:
      """
      {"name": "acme-over", "version": "2.0.0", "main": "index.js"}
      """
    And a file "v2/index.js" with content:
      """
      module.exports = 'two';
      """
    When I run npm "publish ./v1"
    Then the command succeeds
    When I run npm "publish ./v2"
    Then the command succeeds
    When I run npm "dist-tag add acme-over@2.0.0 beta"
    Then the command succeeds
    When I run npm "deprecate acme-over@2.0.0 use{sp}3.0.0"
    Then the command succeeds
    # 0.5.0's tarball deployed by REST over 2.0.0's, as jf rt u could.
    Given an npm package "acme-over" at "0.5.0" with its tarball at "/artifactory/api/npm/npm-over/acme-over/-/acme-over-0.5.0.tgz", under "npm"
    When I deploy the file "npm/acme-over-0.5.0.tgz" to "/artifactory/npm-over/acme-over/-/acme-over-2.0.0.tgz" as admin
    Then the response status is 201
    # The overwritten file keeps none of 2.0.0's properties.
    When I GET "/artifactory/api/storage/npm-over/acme-over/-/acme-over-2.0.0.tgz?properties" as admin
    Then the response status is 404
    And the response body does not contain "npm.deprecated"
    When I run npm "dist-tag ls acme-over"
    Then the command succeeds
    And the output contains "latest: 1.0.0"
    And the output does not contain "beta"
    # The conversion moves the overwritten tarball to 0.5.0's own path.
    When I POST "/artifactory/api/v1/migrations/npm/repositories/npm-over/migrate" as admin
    Then the response status is 202
    When I GET "/artifactory/api/storage/npm-over/acme-over/-/acme-over-0.5.0.tgz" as admin until the response status is 200
    When I run npm "view acme-over versions"
    Then the command succeeds
    And the output contains "0.5.0"
    When I run npm "view acme-over@0.5.0 deprecated"
    Then the command succeeds
    And the output does not contain "use 3.0.0"
    When I run npm "dist-tag ls acme-over"
    Then the command succeeds
    And the output contains "latest: 1.0.0"
    And the output does not contain "beta"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> gets latest from the version published last under npm.tag_latest_by_publish
    Given the registry is running with environment "ARTIFACTWEAVE_NPM_TAG_LATEST_BY_PUBLISH=true"
    And the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-bypub" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-bypub/
      //{host}/artifactory/api/npm/npm-bypub/:_auth={basicauth}
      """
    And a file "v2/package.json" with content:
      """
      {"name": "acme-bypub", "version": "2.0.0", "main": "index.js"}
      """
    And a file "v2/index.js" with content:
      """
      module.exports = 'two';
      """
    And a file "v1/package.json" with content:
      """
      {"name": "acme-bypub", "version": "1.0.0", "main": "index.js"}
      """
    And a file "v1/index.js" with content:
      """
      module.exports = 'one';
      """
    And a file "v3/package.json" with content:
      """
      {"name": "acme-bypub", "version": "3.0.0", "main": "index.js"}
      """
    And a file "v3/index.js" with content:
      """
      module.exports = 'three';
      """
    When I run npm "publish ./v2"
    Then the command succeeds
    # npm refuses a lower version as an implicit latest unless forced; the
    # registry ignores the latest npm sends and applies its own rule.
    When I run npm "publish ./v1 --force"
    Then the command succeeds
    When I run npm "view acme-bypub dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0"
    # A tagged publish leaves latest, as by default.
    When I run npm "publish ./v3 --tag next"
    Then the command succeeds
    When I run npm "dist-tag ls acme-bypub"
    Then the command succeeds
    And the output contains "latest: 1.0.0"
    And the output contains "next: 3.0.0"
    When I run npm "install acme-bypub"
    Then the command succeeds
    And the file "node_modules/acme-bypub/index.js" contains "one"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> publishes under npm.enforce_path, which refuses a tarball uploaded under another file name
    Given the registry is running with environment "ARTIFACTWEAVE_NPM_ENFORCE_PATH=true"
    And the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-enforce" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given an npm package "acme-enforce" at "1.0.0" with its tarball at "/artifactory/api/npm/npm-enforce/acme-enforce/-/acme-enforce-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-enforce-1.0.0.tgz" to "/artifactory/npm-enforce/uploads/acme-enforce.tgz" as admin
    Then the response status is 400
    And the response body contains "acme-enforce-1.0.0.tgz"
    When I deploy the file "npm/acme-enforce-1.0.0.tgz" to "/artifactory/npm-enforce/acme-enforce/-/acme-enforce-1.0.0.tgz" as admin
    Then the response status is 201
    # npm publish names the tarball from the package itself, so a publish,
    # scoped or not, always meets the rule.
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-enforce/
      //{host}/artifactory/api/npm/npm-enforce/:_auth={basicauth}
      """
    And a file "scoped/package.json" with content:
      """
      {"name": "@acme/enforced", "version": "2.0.0", "main": "index.js"}
      """
    And a file "scoped/index.js" with content:
      """
      module.exports = 'scoped';
      """
    When I run npm "publish ./scoped"
    Then the command succeeds
    When I run npm "install acme-enforce @acme/enforced"
    Then the command succeeds
    And the file "node_modules/@acme/enforced/index.js" contains "scoped"
    And the file "node_modules/acme-enforce/package.json" contains "1.0.0"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

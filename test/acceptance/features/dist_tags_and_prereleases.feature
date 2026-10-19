Feature: npm dist-tags across overwrites, latest by publish, and pre-release cleanup
  An overwrite that changes a tarball's version takes the dropped version's
  dist-tags with it, so the strict-layout conversion hands none of them on;
  npm.tag_latest_by_publish gives latest to the version published last, as
  7.x's artifactory.npm.tag.tagLatestByPublish does; and maxUniqueSnapshots
  keeps the newest pre-releases of each version, deleted by the timed
  cleanup Maven's uses (decision 0034, #515). npm runs in the container
  pinned by digest (test/acceptance/clients).

  @npm
  Scenario: An overwrite with another version's tarball takes its dist-tags, and the conversion hands on none
    Given the registry is running
    And the npm client can reach the registry
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
    # 0.5.0's tarball deployed by REST over 2.0.0's, as jf rt u could.
    Given an npm package "acme-over" at "0.5.0" with its tarball at "/artifactory/api/npm/npm-over/acme-over/-/acme-over-0.5.0.tgz", under "npm"
    When I deploy the file "npm/acme-over-0.5.0.tgz" to "/artifactory/npm-over/acme-over/-/acme-over-2.0.0.tgz" as admin
    Then the response status is 201
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
    When I run npm "dist-tag ls acme-over"
    Then the command succeeds
    And the output contains "latest: 1.0.0"
    And the output does not contain "beta"

  @npm
  Scenario: npm.tag_latest_by_publish gives latest to the version published last
    Given the registry is running with environment "ARTIFACTWEAVE_NPM_TAG_LATEST_BY_PUBLISH=true"
    And the npm client can reach the registry
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

  @npm
  Scenario: maxUniqueSnapshots keeps the two newest pre-releases of a version
    Given the registry is running with environment "ARTIFACTWEAVE_MAVEN_SNAPSHOT_CLEANUP_INTERVAL=1s"
    And the npm client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-snaps" as admin with body:
      """
      {"rclass":"local","packageType":"npm","maxUniqueSnapshots":2}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-snaps/
      //{host}/artifactory/api/npm/npm-snaps/:_auth={basicauth}
      """
    And a file "b1/package.json" with content:
      """
      {"name": "acme-snaps", "version": "1.0.0-beta.1", "main": "index.js"}
      """
    And a file "b1/index.js" with content:
      """
      module.exports = 'beta 1';
      """
    And a file "b2/package.json" with content:
      """
      {"name": "acme-snaps", "version": "1.0.0-beta.2", "main": "index.js"}
      """
    And a file "b2/index.js" with content:
      """
      module.exports = 'beta 2';
      """
    And a file "b3/package.json" with content:
      """
      {"name": "acme-snaps", "version": "1.0.0-beta.3", "main": "index.js"}
      """
    And a file "b3/index.js" with content:
      """
      module.exports = 'beta 3';
      """
    When I run npm "publish ./b1 --tag beta"
    Then the command succeeds
    When I run npm "publish ./b2 --tag beta"
    Then the command succeeds
    When I run npm "publish ./b3 --tag beta"
    Then the command succeeds
    Then within 20 seconds GET "/artifactory/npm-snaps/acme-snaps/-/acme-snaps-1.0.0-beta.1.tgz" as admin answers 404
    When I run npm "view acme-snaps versions"
    Then the command succeeds
    And the output contains "1.0.0-beta.2"
    And the output contains "1.0.0-beta.3"
    And the output does not contain "1.0.0-beta.1"
    # beta.1 held latest as the package's first version; it went to the
    # version that outranks the rest.
    When I run npm "view acme-snaps dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0-beta.3"

Feature: npm reads from and publishes to npm local repositories
  The registry at /artifactory/api/npm/<repo>/ builds packuments from the
  package index the npm format keeps (decision 0034), so a tarball
  deployed to its canonical path by REST, as jf npm publish deploys it,
  installs with npm, and so does one npm publish stores. npm runs in the
  containers pinned by digest (test/acceptance/clients): each client
  scenario runs with Node 26's npm 11 and again with npm 12 on Node 24 LTS (#294).

  Background:
    Given the registry is running

  @npm
  Scenario Outline: <npm> installs a package deployed by REST to its canonical path
    Given the <npm> client can reach the registry
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
    And the lockfile pins "acme-read" to the tarball at "/artifactory/api/npm/npm-read/acme-read/-/acme-read-1.0.0.tgz"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> publishes unscoped and scoped packages, and a version only once
    Given the <npm> client can reach the registry
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
    And the lockfile pins "acme-pub" to the tarball at "/artifactory/api/npm/npm-publish/acme-pub/-/acme-pub-1.0.0.tgz"
    And the lockfile pins "@acme/scoped-pub" to the tarball at "/artifactory/api/npm/npm-publish/@acme/scoped-pub/-/@acme/scoped-pub-2.0.0.tgz"
    # npm checks for the version itself unless --force is given; the
    # registry refuses it either way.
    When I run npm "publish ./acme-pub --force"
    Then the command fails
    And the output contains "403"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> publish meets the release and snapshot handling, and npm revalidates packuments
    Given the <npm> client can reach the registry
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

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> moves dist-tags, installs by tag, and deprecates a version
    Given the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-tags" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-tags/
      //{host}/artifactory/api/npm/npm-tags/:_auth={basicauth}
      """
    And a file "v1/package.json" with content:
      """
      {"name": "acme-tags", "version": "1.0.0", "main": "index.js"}
      """
    And a file "v1/index.js" with content:
      """
      module.exports = 'one';
      """
    And a file "beta/package.json" with content:
      """
      {"name": "acme-tags", "version": "2.0.0-beta.1", "main": "index.js"}
      """
    And a file "beta/index.js" with content:
      """
      module.exports = 'beta';
      """
    And a file "v3/package.json" with content:
      """
      {"name": "acme-tags", "version": "3.0.0", "main": "index.js"}
      """
    And a file "v3/index.js" with content:
      """
      module.exports = 'three';
      """
    When I run npm "publish ./v1"
    Then the command succeeds
    When I run npm "publish ./beta --tag beta"
    Then the command succeeds
    # A tagged publish of a higher version leaves latest, as 7.x does
    # (RTFACT-24072).
    When I run npm "publish ./v3 --tag next"
    Then the command succeeds
    When I run npm "view acme-tags dist-tags.latest"
    Then the command succeeds
    And the output contains "1.0.0"
    # latest is stored as 7.x stores it, as npm.disttag on its tarball.
    When I GET "/artifactory/api/storage/npm-tags/acme-tags/-/acme-tags-1.0.0.tgz?properties" as admin
    Then the response status is 200
    And the response body contains "npm.disttag"
    And the response body contains "latest"
    When I run npm "dist-tag add acme-tags@2.0.0-beta.1 stable"
    Then the command succeeds
    When I run npm "dist-tag ls acme-tags"
    Then the command succeeds
    And the output contains "stable: 2.0.0-beta.1"
    And the output contains "next: 3.0.0"
    And the output contains "latest: 1.0.0"
    When I run npm "install acme-tags@stable"
    Then the command succeeds
    And the file "node_modules/acme-tags/index.js" contains "beta"
    When I run npm "dist-tag rm acme-tags stable"
    Then the command succeeds
    When I run npm "dist-tag ls acme-tags"
    Then the command succeeds
    And the output does not contain "stable"
    When I run npm "deprecate acme-tags@1.0.0 use{sp}3.0.0"
    Then the command succeeds
    When I run npm "view acme-tags@1.0.0 deprecated"
    Then the command succeeds
    And the output contains "use 3.0.0"
    When I GET "/artifactory/api/storage/npm-tags/acme-tags/-/acme-tags-1.0.0.tgz?properties" as admin
    Then the response body contains "npm.deprecated"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @npm
  Scenario Outline: <npm> unpublishes one version, then the whole package with --force
    Given the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-gone" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-gone/
      //{host}/artifactory/api/npm/npm-gone/:_auth={basicauth}
      """
    And a file "v1/package.json" with content:
      """
      {"name": "acme-gone", "version": "1.0.0", "main": "index.js"}
      """
    And a file "v1/index.js" with content:
      """
      module.exports = 'one';
      """
    And a file "v2/package.json" with content:
      """
      {"name": "acme-gone", "version": "1.1.0", "main": "index.js"}
      """
    And a file "v2/index.js" with content:
      """
      module.exports = 'two';
      """
    When I run npm "publish ./v1"
    Then the command succeeds
    When I run npm "publish ./v2"
    Then the command succeeds
    When I run npm "unpublish acme-gone@1.1.0"
    Then the command succeeds
    When I run npm "view acme-gone versions"
    Then the command succeeds
    And the output contains "1.0.0"
    And the output does not contain "1.1.0"
    # latest moved on to the version left.
    When I run npm "view acme-gone dist-tags.latest"
    Then the output contains "1.0.0"
    # Through the trash can.
    When I GET "/artifactory/api/storage/auto-trashcan/npm-gone/acme-gone/-/acme-gone-1.1.0.tgz" as admin
    Then the response status is 200
    When I run npm "unpublish acme-gone --force"
    Then the command succeeds
    When I run npm "view acme-gone"
    Then the command fails
    And the output contains "404"

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  # The conversion's own client is the REST API; jf rt u stores a tarball as
  # the REST deploy below does.
  Scenario: The strict-layout conversion indexes a tarball deployed off its canonical path
    When I PUT "/artifactory/api/repositories/npm-conv" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given an npm package "acme-conv" at "1.0.0" with its tarball at "/artifactory/api/npm/npm-conv/acme-conv/-/acme-conv-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-conv-1.0.0.tgz" to "/artifactory/npm-conv/uploads/acme-conv-1.0.0.tgz" as admin
    Then the response status is 201
    # Strict layout stores it but does not index it.
    When I GET "/artifactory/api/npm/npm-conv/acme-conv" as admin
    Then the response status is 404
    When I POST "/artifactory/api/v1/migrations/npm/repositories/npm-conv/migrate" as admin
    Then the response status is 202
    When I GET "/artifactory/api/npm/npm-conv/acme-conv" as admin until the response status is 200
    Then the response body contains "/artifactory/api/npm/npm-conv/acme-conv/-/acme-conv-1.0.0.tgz"
    When I GET "/artifactory/api/storage/npm-conv/acme-conv/-/acme-conv-1.0.0.tgz" as admin
    Then the response status is 200
    When I GET "/artifactory/api/storage/npm-conv/uploads/acme-conv-1.0.0.tgz" as admin
    Then the response status is 404
    # Nothing failed, so there is nothing to resume.
    When I POST "/artifactory/api/v1/migrations/npm/repositories/npm-conv/resume" as admin
    Then the response status is 400
    # The reindex is queued too, and answers 7.x's text.
    When I POST "/artifactory/api/npm/npm-conv/reindex" as admin
    Then the response status is 200
    And the response body is "Recalculating index for npm repository npm-conv scheduled to run"

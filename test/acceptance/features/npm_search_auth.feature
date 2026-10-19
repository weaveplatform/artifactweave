Feature: npm search, login and logout, and the JFrog CLI's npm configuration
  npm search reads the package index each deploy keeps current, so a package
  is found once its publish commits, filtered by what the caller may read.
  npm login falls back from web login, which waits for #262, to the legacy
  login, which checks the password as any password login is checked and
  issues an access token; npm logout revokes it. The JFrog CLI's npm
  commands ask GET /api/npm/auth for the lines that authenticate npm
  (decision 0034). npm runs in the container pinned by digest
  (test/acceptance/clients); jf runs the runner's own npm.

  Background:
    Given the registry is running

  @npm
  Scenario Outline: <npm> search finds what the caller may read, and npm logs in and out
    Given the <npm> client can reach the registry
    When I PUT "/artifactory/api/repositories/npm-search" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    # ann reads acme-* alone: a new user joins readers, which reads
    # everything, so she leaves it.
    When I PUT "/artifactory/api/security/users/ann" as admin with body:
      """
      {"name": "ann", "email": "ann@example.com", "password": "Ann-pass-1"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/groups/readers/members" as admin with JSON:
      """
      {"remove": ["ann"]}
      """
    And I POST "/access/api/v2/permissions" as admin with body:
      """
      {"name": "ann-acme", "resources": {"artifact": {
        "actions": {"users": {"ann": ["READ"]}},
        "targets": {"npm-search": {"include_patterns": ["acme-*/**"], "exclude_patterns": []}}}}}
      """
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-search/
      //{host}/artifactory/api/npm/npm-search/:_auth={basicauth}
      """
    And a file "acme/package.json" with content:
      """
      {"name": "acme-widget", "version": "1.0.0", "description": "Builds widgets", "keywords": ["widget"]}
      """
    And a file "hidden/package.json" with content:
      """
      {"name": "hidden-widget", "version": "2.0.0", "keywords": ["widget"]}
      """
    When I run npm "publish ./acme"
    Then the command succeeds
    When I run npm "publish ./hidden"
    Then the command succeeds
    # Found at once: the publish updated the index it searches.
    When I run npm "search widget"
    Then the command succeeds
    And the output contains "acme-widget"
    And the output contains "hidden-widget"
    And the output contains "Builds widgets"
    When I run npm "search --json builds"
    Then the command succeeds
    And the output contains "acme-widget"
    And the output does not contain "hidden-widget"
    # ann logs in with npm login. The web login it tries first is not
    # served, so npm falls back to the legacy login and prompts.
    Given a file ".npmrc" with expanded content:
      """
      registry={url}/api/npm/npm-search/
      """
    When I run npm "login", answering its prompts with:
      """
      ann
      not-her-password
      """
    Then the command fails
    And the server answered 401 to PUT "/artifactory/api/npm/npm-search/-/user/org.couchdb.user:ann"
    When I run npm "login", answering its prompts with:
      """
      ann
      Ann-pass-1
      """
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/api/npm/npm-search/-/user/org.couchdb.user:ann"
    And the file ".npmrc" contains "/artifactory/api/npm/npm-search/:_authToken="
    When I run npm "whoami"
    Then the command succeeds
    And the output contains "ann"
    When I run npm "search widget"
    Then the command succeeds
    And the output contains "acme-widget"
    And the output does not contain "hidden-widget"
    # npm logout revokes the token; the access log never records it.
    When I run npm "logout"
    Then the command succeeds
    And the server answered 200 to DELETE "/artifactory/api/npm/npm-search/-/user/token/[redacted]"
    When I run npm "whoami"
    Then the command fails

    Examples:
      | npm    |
      | npm    |
      | npm 12 |

  @jf @npm
  Scenario: jf npm install with a user name and password asks api/npm/auth for npm's credentials
    Given the JFrog CLI is available
    And npm is on the runner's PATH
    When I PUT "/artifactory/api/repositories/npm-jf" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given an npm package "acme-jf" at "1.0.0" with its tarball at "/artifactory/api/npm/npm-jf/acme-jf/-/acme-jf-1.0.0.tgz", under "npm"
    When I deploy the file "npm/acme-jf-1.0.0.tgz" to "/artifactory/npm-jf/acme-jf/-/acme-jf-1.0.0.tgz" as admin
    Then the response status is 201
    Given a file "app/package.json" with content:
      """
      {"name": "app", "version": "1.0.0", "dependencies": {"acme-jf": "1.0.0"}}
      """
    # --basic-auth-only keeps the password rather than swapping it for an
    # access token, which jf would write as _authToken itself.
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --basic-auth-only --interactive=false"
    Then the command succeeds
    When I run jf "npm-config --server-id-resolve aw --repo-resolve npm-jf" in "app"
    Then the command succeeds
    When I run jf "npm install" in "app"
    Then the command succeeds
    And the server answered 200 to GET "/artifactory/api/npm/auth"
    And the file "app/node_modules/acme-jf/package.json" contains "1.0.0"

  # The JFrog CLI's part of the P7 npm exit checks (#294). jf npm publish
  # packs with the runner's npm and deploys the tarball by REST with the
  # build's properties, and with --tag adds npm.disttag. jf npm install
  # records what it installed. In native mode (JFROG_RUN_NATIVE, which
  # replaces the deprecated --run-native) npm publishes with the project's
  # own .npmrc, from which jf reads the registry of the package's scope.
  # jf rt bp then publishes all four modules.
  @jf @npm
  Scenario: jf npm publish and jf npm install with a build, then jf rt bp
    Given the JFrog CLI is available
    And npm is on the runner's PATH
    When I PUT "/artifactory/api/repositories/npm-jf" as admin with body:
      """
      {"rclass":"local","packageType":"npm"}
      """
    Then the response status is 200
    Given a file "lib/package.json" with content:
      """
      {"name": "acme-jf-lib", "version": "1.0.0", "main": "index.js"}
      """
    And a file "lib/index.js" with content:
      """
      module.exports = 'published by jf';
      """
    And a file "beta/package.json" with content:
      """
      {"name": "acme-jf-lib", "version": "1.1.0-beta.1", "main": "index.js"}
      """
    And a file "beta/index.js" with content:
      """
      module.exports = 'beta';
      """
    And a file "app/package.json" with content:
      """
      {"name": "acme-jf-app", "version": "1.0.0", "dependencies": {"acme-jf-lib": "1.0.0"}}
      """
    And a file "native/package.json" with content:
      """
      {"name": "@acme/jf-native", "version": "1.0.0", "main": "index.js"}
      """
    And a file "native/index.js" with content:
      """
      module.exports = 'published by npm for jf';
      """
    And a file "native/.npmrc" with expanded content:
      """
      @acme:registry={url}/api/npm/npm-jf/
      //{host}/artifactory/api/npm/npm-jf/:_auth={basicauth}
      """
    When I run jf "c add aw --url {platform}/ --artifactory-url {url}/ --user admin --password {password} --basic-auth-only --interactive=false"
    Then the command succeeds
    When I run jf "npm-config --server-id-resolve aw --server-id-deploy aw --repo-resolve npm-jf --repo-deploy npm-jf" in "lib"
    Then the command succeeds
    When I run jf "npm-config --server-id-resolve aw --server-id-deploy aw --repo-resolve npm-jf --repo-deploy npm-jf" in "beta"
    Then the command succeeds
    When I run jf "npm-config --server-id-resolve aw --repo-resolve npm-jf" in "app"
    Then the command succeeds
    When I run jf "npm publish --build-name npm-accept --build-number 1" in "lib"
    Then the command succeeds
    When I run jf "npm publish --tag beta --build-name npm-accept --build-number 1" in "beta"
    Then the command succeeds
    When I run jf "npm install --build-name npm-accept --build-number 1" in "app"
    Then the command succeeds
    And the file "app/node_modules/acme-jf-lib/index.js" contains "published by jf"
    Given the JFrog CLI runs with "JFROG_RUN_NATIVE" set to "true"
    When I run jf "npm publish --build-name npm-accept --build-number 1" in "native"
    Then the command succeeds
    And the server answered 201 to PUT "/artifactory/api/npm/npm-jf/@acme/jf-native"
    When I run jf "rt bp npm-accept 1 --server-id aw"
    Then the command succeeds
    When I GET "/artifactory/api/build/npm-accept/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules" has 4 entries
    And the response body contains "acme-jf-lib:1.0.0"
    And the response body contains "acme-jf-lib:1.1.0-beta.1"
    And the response body contains "acme-jf-app:1.0.0"
    And the response body contains "jf-native:1.0.0"
    When I GET "/artifactory/api/storage/npm-jf/acme-jf-lib/-/acme-jf-lib-1.0.0.tgz?properties" as admin
    Then the response status is 200
    And the response body contains "build.name"
    And the response body contains "npm-accept"
    When I GET "/artifactory/api/npm/npm-jf/-/package/acme-jf-lib/dist-tags" as admin
    Then the response status is 200
    And the JSON field "beta" equals "1.1.0-beta.1"
    When I GET "/artifactory/api/npm/npm-jf/@acme%2fjf-native" as admin
    Then the response status is 200
    And the response body contains "/artifactory/api/npm/npm-jf/@acme/jf-native/-/@acme/jf-native-1.0.0.tgz"

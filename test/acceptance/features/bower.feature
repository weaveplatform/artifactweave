@bower
Feature: Native Bower client resolution
  Bower 1.8.14 with JFrog bower-art-resolver 2.0.10 resolves an indexed
  package through registry lookup, refs and a gzip tar binary.

  Scenario Outline: one-node <class> publication and Bower install
    Given the registry is running
    And the Bower client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"bower"}
      """
    Then the response status is 200
    Given a Bower package "hello" version "1.0.0" with marker "<repo>" under "bower-fixture"
    When I deploy the files under "bower-fixture" to "/artifactory/<repo>" as admin
    Then the response status is 201
    Given a Bower client project for repository "<repo>"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "<repo>"

    Examples:
      | class     | repo        |
      | local     | bower-local |
      | federated | bower-fed   |

  Scenario: virtual Bower selects its first member for an overlapping version
    Given the registry is running
    And the Bower client can reach the registry
    When I PUT "/artifactory/api/repositories/bower-first" as admin with body:
      """
      {"rclass":"local","packageType":"bower"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/bower-second" as admin with body:
      """
      {"rclass":"local","packageType":"bower"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/bower-combined" as admin with body:
      """
      {"rclass":"virtual","packageType":"bower","repositories":["bower-first","bower-second"]}
      """
    Then the response status is 200
    Given a Bower package "hello" version "1.0.0" with marker "first" under "first-fixture"
    When I deploy the files under "first-fixture" to "/artifactory/bower-first" as admin
    Then the response status is 201
    Given a Bower package "hello" version "1.0.0" with marker "second" under "second-fixture"
    When I deploy the files under "second-fixture" to "/artifactory/bower-second" as admin
    Then the response status is 201
    Given a Bower client project for repository "bower-combined"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "first"

  Scenario: a fresh Bower install uses the remote cache while offline
    Given the registry is running
    And the Bower client can reach the registry
    And a Bower upstream serves package "hello" version "1.0.0" with marker "upstream" for repository "bower-remote"
    When I PUT "/artifactory/api/repositories/bower-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"{upstream}/bower/","vcsGitProvider":"ARTIFACTORY"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-remote"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "upstream"
    And the generic upstream received 1 GET requests for "/bower/binaries/bower-remote/hello.git/v1.0.0"
    When I POST "/artifactory/api/repositories/bower-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I clear the Bower client cache and installation
    And I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "upstream"

  Scenario: delete, restore and reindex update Bower discovery
    Given the registry is running
    And the Bower client can reach the registry
    When I PUT "/artifactory/api/repositories/bower-life" as admin with body:
      """
      {"rclass":"local","packageType":"bower"}
      """
    Then the response status is 200
    Given a Bower package "hello" version "1.0.0" with marker "restored" under "life-fixture"
    When I deploy the files under "life-fixture" to "/artifactory/bower-life" as admin
    Then the response status is 201
    When I GET "/artifactory/api/bower/bower-life/packages/hello" as admin
    Then the response status is 200
    When I DELETE "/artifactory/bower-life/hello/1.0.0/bower.json" as admin
    Then the response status is 204
    When I GET "/artifactory/api/bower/bower-life/packages/hello" as admin
    Then the response status is 404
    When I POST "/artifactory/api/trash/restore/bower-life/hello/1.0.0/bower.json?to=bower-life" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/bower/bower-life/reindex" as admin
    Then the response status is 200
    And the queued reindex of "bower-life" completes within 20 seconds
    Given a Bower client project for repository "bower-life"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "restored"

  Scenario: remote metadata refresh discovers a new tag after a same-origin redirect
    Given the registry is running
    And the Bower client can reach the registry
    And a Bower upstream serves package "hello" version "1.0.0" with marker "initial" for repository "bower-refresh"
    And the Bower upstream redirects version "1.0.0" for repository "bower-refresh"
    When I PUT "/artifactory/api/repositories/bower-refresh" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"{upstream}/bower/","vcsGitProvider":"ARTIFACTORY"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-refresh"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "redirected"
    Given the Bower upstream adds version "2.0.0" with marker "refreshed" for repository "bower-refresh"
    When I POST "/artifactory/api/zap/bower-refresh-cache/catalog/hello" as admin with body:
      """
      """
    Then the response status is 200
    When I clear the Bower client cache and installation
    And I run bower "install hello#2.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "refreshed"

  Scenario: an invalid upstream archive cannot become an installed or cached package
    Given the registry is running
    And the Bower client can reach the registry
    And a Bower upstream serves package "hello" version "1.0.0" with marker "good" for repository "bower-bad"
    And the Bower upstream corrupts version "1.0.0" for repository "bower-bad"
    When I PUT "/artifactory/api/repositories/bower-bad" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"{upstream}/bower/","vcsGitProvider":"ARTIFACTORY"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-bad"
    When I run bower "install hello#1.0.0 --force"
    Then the command fails
    When I GET "/artifactory/bower-bad-cache/hello/1.0.0/bower-archive.tar.gz" as admin
    Then the response status is 404

  Scenario: a remote Git tag without a v prefix resolves through the native registry
    Given the registry is running
    And the Bower client can reach the registry
    And a Bower upstream serves package "hello" version "1.0.0" with marker "initial" for repository "bower-plain"
    And the Bower upstream uses a plain tag for version "1.0.0" in repository "bower-plain"
    When I PUT "/artifactory/api/repositories/bower-plain" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"{upstream}/bower/","vcsGitProvider":"ARTIFACTORY"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-plain"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "plain-tag"
    And the generic upstream received 1 GET requests for "/bower/binaries/bower-plain/hello.git/1.0.0"

  Scenario Outline: <kind> Git registry pointer resolves through the native remote cache
    Given a <kind> Git-backed Bower upstream serves package "hello" version "1.0.0" with marker "git-<kind>"
    And the registry is running
    And the Bower client can reach the registry
    When I PUT "/artifactory/api/repositories/bower-git-<kind>" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"{upstream}/git/","bowerRegistryUrl":"{upstream}/registry","vcsGitProvider":"GITHUB","username":"<username>","password":"<password>"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-git-<kind>"
    When I GET "/artifactory/api/bower/bower-git-<kind>/packages/hello" as admin
    Then the response status is 200
    When I GET "/artifactory/api/bower/bower-git-<kind>/packages/search/hello" as admin
    Then the response status is 200
    And the response body contains "hello"
    When I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "git-<kind>"
    And the generic upstream received 1 GET requests for "/git/testorg/hello.git/info/refs"
    And the generic upstream received 1 GET requests for "/git/testorg/hello/archive/refs/tags/v1.0.0.tar.gz"
    When I POST "/artifactory/api/repositories/bower-git-<kind>" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I clear the Bower client cache and installation
    And I run bower "install hello#1.0.0 --force"
    Then the command succeeds
    And the file "bower_components/hello/index.js" contains "git-<kind>"

    Examples:
      | kind    | username | password   |
      | public  |          |            |
      | private | git-user | git-secret |

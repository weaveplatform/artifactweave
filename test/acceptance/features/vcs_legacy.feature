@vcs @vcslegacy @maven
Feature: Legacy VCS providers preserve the native archive and reference contract
  Provider-specific upstream endpoints share the remote source cache and authorization.

  Scenario Outline: the pinned curl client reads public and private VCS provider sources
    Given the registry is running
    And the JVM recipe client image is available
    And a "<provider>" VCS upstream is "<visibility>" with marker "legacy-vcs"
    When I PUT "/artifactory/api/repositories/vcs-legacy" as admin with body:
      """
      {"rclass":"remote","packageType":"vcs","url":"{vcs-url}","vcsGitProvider":"<provider>","username":"<username>","password":"<password>"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "vcs-token"
    When I run the pinned VCS client for "tags/vcs-legacy/team/project" with token "invalid-vcs-token"
    Then the command fails
    And the VCS upstream received 0 reference requests
    When I run the pinned VCS client for "tags/vcs-legacy/team/project" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "tags" with marker "legacy-vcs"
    When I run the pinned VCS client for "branches/vcs-legacy/team/project" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "branches" with marker "legacy-vcs"
    When I run the pinned VCS client for "refs/vcs-legacy/team/project" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "refs" with marker "legacy-vcs"
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "archive" with marker "legacy-vcs"
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!index.js?ext=zip" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "file" with marker "legacy-vcs"
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!README.md?ext=zip" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS file "README.md" contains exactly "root readme"
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!sub/README.md?ext=zip" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS file "sub/README.md" contains exactly "nested readme"
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!ONLY.md?ext=zip" with token "{vcs-token}"
    Then the command fails
    And the server answered 404 to GET "/artifactory/api/vcs/downloadBranchFile/vcs-legacy/team/project/main!ONLY.md"
    Given the VCS upstream advances to marker "refreshed-vcs"
    When I POST "/artifactory/api/zap/vcs-legacy-cache/team/project/refs" as admin with body:
      """
      """
    Then the response status is 200
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "archive" with marker "refreshed-vcs"
    And the VCS upstream received 2 reference requests
    When the mutable peer VCS selected-file cache is removed
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!index.js?ext=zip" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "file" with marker "refreshed-vcs"
    Given the VCS "refs" upstream response becomes "malformed"
    When I POST "/artifactory/api/zap/vcs-legacy-cache/team/project/refs" as admin with body:
      """
      """
    Then the response status is 200
    When I run the pinned VCS client for "tags/vcs-legacy/team/project" with token "{vcs-token}"
    Then the command fails
    And the server answered 502 to GET "/artifactory/api/vcs/tags/vcs-legacy/team/project"
    Given the VCS upstream advances to marker "refreshed-vcs"
    When I POST "/artifactory/api/zap/vcs-legacy-cache/team/project/refs" as admin with body:
      """
      """
    Then the response status is 200
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "archive" with marker "refreshed-vcs"
    When I POST "/artifactory/api/repositories/vcs-legacy" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I run the pinned VCS client for "downloadBranchFile/vcs-legacy/team/project/main!index.js?ext=zip" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "file" with marker "refreshed-vcs"
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "invalid-vcs-token"
    Then the command fails
    And the server answered 401 to GET "/artifactory/api/vcs/downloadTag/vcs-legacy/team/project/v1.0.0"
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "{vcs-token}"
    Then the command succeeds
    And the pinned VCS client received "archive" with marker "refreshed-vcs"
    When I DELETE "/artifactory/vcs-legacy-cache/team/project/refs/tags/v1.0.0/project-{vcs-oid}.tar.gz" as admin
    Then the response status is 204
    When I run the pinned VCS client for "downloadTag/vcs-legacy/team/project/v1.0.0" with token "{vcs-token}"
    Then the command fails
    And the server answered 404 to GET "/artifactory/api/vcs/downloadTag/vcs-legacy/team/project/v1.0.0"

    Examples:
      | provider | visibility | username    | password             |
      | STASH    | public     |             |                      |
      | STASH    | private    | vcs-fixture | private-fixture-pass |
      | OLDSTASH | public | | |
      | OLDSTASH | private | vcs-fixture | private-fixture-pass |
      | ARTIFACTORY | public | | |
      | ARTIFACTORY | private | vcs-fixture | private-fixture-pass |

  Scenario Outline: invalid provider responses are rejected before cache admission
    Given the registry is running
    And the JVM recipe client image is available
    And a "<provider>" VCS upstream is "public" with marker "legacy-vcs"
    And the VCS "<kind>" upstream response becomes "<fault>"
    When I PUT "/artifactory/api/repositories/vcs-legacy" as admin with body:
      """
      {"rclass":"remote","packageType":"vcs","url":"{vcs-url}","vcsGitProvider":"<provider>"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "vcs-token"
    When I run the pinned VCS client for "<operation>/vcs-legacy/team/project<suffix>" with token "{vcs-token}"
    Then the command fails
    And the server answered <status> to GET "/artifactory/api/vcs/<operation>/vcs-legacy/team/project<suffix>"
    And the output does not contain "invalid-vcs-secret-fixture"
    And the generic upstream received 0 GET requests for "/git/other-project/archive"

    Examples:
      | provider | kind | fault | operation | suffix | status |
      | STASH | refs | malformed | tags | | 502 |
      | STASH | archive | malformed | downloadTag | /v1.0.0 | 502 |
      | STASH | refs | unavailable | tags | | 503 |
      | STASH | archive | redirect | downloadTag | /v1.0.0 | 502 |
      | OLDSTASH | refs | malformed | tags | | 502 |
      | OLDSTASH | archive | malformed | downloadTag | /v1.0.0 | 502 |
      | OLDSTASH | refs | unavailable | tags | | 503 |
      | OLDSTASH | archive | redirect | downloadTag | /v1.0.0 | 502 |
      | ARTIFACTORY | refs | malformed | tags | | 502 |
      | ARTIFACTORY | archive | malformed | downloadTag | /v1.0.0 | 502 |
      | ARTIFACTORY | refs | unavailable | tags | | 503 |
      | ARTIFACTORY | archive | redirect | downloadTag | /v1.0.0 | 502 |
      | STASH | archive | wrong-commit | downloadTag | /v1.0.0 | 502 |
      | OLDSTASH | archive | wrong-commit | downloadTag | /v1.0.0 | 502 |
      | ARTIFACTORY | archive | wrong-commit | downloadTag | /v1.0.0 | 502 |
      | ARTIFACTORY | file | malformed | downloadBranchFile | /main!index.js | 502 |
      | ARTIFACTORY | file | unavailable | downloadBranchFile | /main!index.js | 503 |
      | ARTIFACTORY | file | redirect | downloadBranchFile | /main!index.js | 502 |

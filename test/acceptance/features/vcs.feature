@vcs
Feature: Remote VCS REST access
  VCS is a remote-only source archive and reference service.

  Scenario: an archive remains available from cache after the upstream stops
    Given the registry is running
    And a VCS Git upstream serves a tagged project with marker "cached-vcs"
    When I PUT "/artifactory/api/repositories/vcs-git" as admin with body:
      """
      {"rclass":"remote","packageType":"vcs","url":"{upstream}/git","vcsGitProvider":"GITHUB"}
      """
    Then the response status is 200
    When I GET "/artifactory/api/vcs/downloadTag/vcs-git/team/project/v1.0.0" as admin
    Then the response status is 200
    And the VCS archive contains marker "cached-vcs"
    When I POST "/artifactory/api/repositories/vcs-git" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the generic upstream stops
    When I GET "/artifactory/api/vcs/downloadTag/vcs-git/team/project/v1.0.0" as admin
    Then the response status is 200
    And the VCS archive contains marker "cached-vcs"
    When I POST "/artifactory/api/zap/vcs-git-cache/team/project/refs" as admin with body:
      """
      """
    Then the response status is 200
    When I GET "/artifactory/api/vcs/downloadTag/vcs-git/team/project/v1.0.0" as admin
    Then the response status is 200
    And the VCS archive contains marker "cached-vcs"
    When I DELETE "/artifactory/vcs-git-cache/team/project/refs/tags/v1.0.0/project-0123456789abcdef0123456789abcdef01234567.tar.gz" as admin
    Then the response status is 204
    When I GET "/artifactory/api/vcs/downloadTag/vcs-git/team/project/v1.0.0" as admin
    Then the response status is 404

  Scenario: a caller without read permission cannot discover remote refs
    Given the registry is running
    When I PUT "/artifactory/api/repositories/vcs-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"vcs","url":"https://github.com","vcsGitProvider":"GITHUB"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/vcs-denied" as admin with body:
      """
      {"name":"vcs-denied","email":"vcs-denied@example.invalid","password":"Denied-pass-123"}
      """
    Then the response status is 201
    When I PATCH "/access/api/v2/groups/readers/members" as admin with JSON:
      """
      {"remove":["vcs-denied"]}
      """
    When I GET "/artifactory/api/vcs/tags/vcs-remote/owner/project" as "vcs-denied" with password "Denied-pass-123"
    Then the response status is 403

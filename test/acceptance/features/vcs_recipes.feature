@recipes @vcs @maven
Feature: The VCS archive recipe runs the pinned HTTP client
  curl starts without a user curlrc and writes to a fresh destination.

  Scenario: the curl archive recipe rejects an invalid token and returns exact source bytes
    Given the registry is running
    And the JVM recipe client image is available
    And a VCS Git upstream serves a tagged project with marker "native-vcs-recipe"
    When I PUT "/artifactory/api/repositories/vcs-recipe" as admin with body:
      """
      {"rclass":"remote","packageType":"vcs","url":"{upstream}/git","vcsGitProvider":"GITHUB"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "recipe-token"
    When I download the VCS recipe with token "invalid-recipe-token"
    Then the command fails
    And the server answered 401 to GET "/artifactory/api/vcs/downloadTag/vcs-recipe/team/project/v1.0.0"
    When I download the VCS recipe with token "{recipe-token}"
    Then the command succeeds
    And the VCS recipe archive contains marker "native-vcs-recipe"
    And the server answered 200 to GET "/artifactory/api/vcs/downloadTag/vcs-recipe/team/project/v1.0.0"

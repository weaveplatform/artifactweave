@bowerlive
Feature: Live public Bower registry and GitHub source probe
  This opt-in probe checks the actual registry.bower.io pointer and GitHub
  smart Git/archive behavior with the pinned Bower client. It is excluded from
  the deterministic @bower CI lane because it depends on external availability.

  Scenario: Bower resolves the public Bootstrap Git pointer through a remote repository
    Given the registry is running
    And the Bower client can reach the registry
    When I PUT "/artifactory/api/repositories/bower-live" as admin with body:
      """
      {"rclass":"remote","packageType":"bower","url":"https://github.com/","bowerRegistryUrl":"https://registry.bower.io","vcsGitProvider":"GITHUB"}
      """
    Then the response status is 200
    Given a Bower client project for repository "bower-live"
    When I GET "/artifactory/api/bower/bower-live/packages/bootstrap" as admin
    Then the response status is 200
    When I run bower "install bootstrap#3.4.1 --force"
    Then the command succeeds
    And the file "bower_components/bootstrap/dist/css/bootstrap.css" contains "Bootstrap v3.4.1"

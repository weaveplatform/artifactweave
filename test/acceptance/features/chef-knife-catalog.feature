@chef
Feature: Knife lists and searches Chef cookbooks

  # knife artifactory list and search ask for items=9999999 to avoid paging
  # (#1305). A search looks in each cookbook's name, description and
  # maintainer (JFrog, "Search for Chef Cookbooks").
  Scenario: Knife lists and searches a local repository's cookbooks
    Given the registry is running
    And the Chef client can reach the registry
    And the Chef cookbook sources are ready
    When I PUT "/artifactory/api/repositories/chef-local" as admin with body:
      """
      {"rclass":"local","packageType":"chef"}
      """
    Then the response status is 200
    When I publish the Chef cookbooks to "chef-local"
    When I list the Chef cookbooks in "chef-local"
    Then the command succeeds
    And the output contains "helper"
    And the output contains "widget"
    When I search the Chef cookbooks in "chef-local" for "helper"
    Then the command succeeds
    And the output contains "cookbooks/helper"
    And the output does not contain "widget"
    When I search the Chef cookbooks in "chef-local" for "ArtifactWeave"
    Then the command succeeds
    And the output contains "cookbooks/helper"
    And the output contains "cookbooks/widget"

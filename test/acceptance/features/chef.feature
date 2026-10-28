@chef
Feature: Knife publishes and installs Chef cookbooks through native Supermarket metadata

  Scenario Outline: Knife publishes, installs dependencies and unshares a cookbook
    Given the registry is running
    And the Chef client can reach the registry
    And the Chef cookbook sources are ready
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"chef"}
      """
    Then the response status is 200
    When I publish the Chef cookbooks to "<repo>"
    When I POST "/artifactory/api/chef/<repo>/reindex" as admin
    Then the response status is 200
    When I install Chef cookbook "widget" from "<repo>" into "installed"
    Then the command succeeds
    And the file "installed/widget/metadata.json" contains "widget"
    And the file "installed/helper/metadata.json" contains "helper"
    When I unshare Chef cookbook "widget" version "1.0.0" from "<repo>"
    Then the command succeeds
    When I GET "/artifactory/api/chef/<repo>/api/v1/cookbooks/widget" as admin
    Then the response status is 404
    Examples:
      | class     | repo       |
      | local     | chef-local |
      | federated | chef-fed   |

  Scenario: Knife follows remote download redirects and installs again while offline
    Given the registry is running
    And the Chef client can reach the registry
    And the Chef cookbook sources are ready
    When I PUT "/artifactory/api/repositories/chef-seed" as admin with body:
      """
      {"rclass":"local","packageType":"chef"}
      """
    Then the response status is 200
    When I publish the Chef cookbooks to "chef-seed"
    Given a Chef upstream serves the cookbooks published in "chef-seed"
    When I PUT "/artifactory/api/repositories/chef-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"chef","url":"{upstream}/chef"}
      """
    Then the response status is 200
    When I install Chef cookbook "widget" from "chef-remote" into "remote-installed"
    Then the command succeeds
    And the file "remote-installed/helper/metadata.json" contains "helper"
    When I POST "/artifactory/api/zap/chef-remote-cache/api/v1/cookbooks" as admin
    Then the response status is 200
    When I install Chef cookbook "widget" from "chef-remote" into "refreshed-installed"
    Then the command succeeds
    And the Chef upstream metadata was refreshed
    When I POST "/artifactory/api/repositories/chef-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the Chef upstream stops
    When I install Chef cookbook "widget" from "chef-remote" into "offline-installed"
    Then the command succeeds
    And the file "offline-installed/helper/metadata.json" contains "helper"

  Scenario: Knife resolves cookbooks through a virtual repository
    Given the registry is running
    And the Chef client can reach the registry
    And the Chef cookbook sources are ready
    When I PUT "/artifactory/api/repositories/chef-local" as admin with body:
      """
      {"rclass":"local","packageType":"chef"}
      """
    Then the response status is 200
    When I publish Chef cookbook "widget" to "chef-local"
    When I PUT "/artifactory/api/repositories/chef-helper" as admin with body:
      """
      {"rclass":"federated","packageType":"chef"}
      """
    Then the response status is 200
    When I publish Chef cookbook "helper" to "chef-helper"
    When I PUT "/artifactory/api/repositories/chef-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"chef","repositories":["chef-local","chef-helper"]}
      """
    Then the response status is 200
    When I install Chef cookbook "widget" from "chef-virtual" into "virtual-installed"
    Then the command succeeds
    And the file "virtual-installed/helper/metadata.json" contains "helper"

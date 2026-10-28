@puppet
Feature: Puppet installs modules and their dependencies through native Forge metadata

  Scenario Outline: fresh Puppet installs from local and one-node federated repositories
    Given the registry is running
    And the Puppet client can reach the registry
    And the Puppet fixtures are ready
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"puppet"}
      """
    Then the response status is 200
    When I deploy the file "acme-widget-1.0.0.tar.gz" to "/artifactory/<repo>/acme-widget/acme-widget-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I deploy the file "acme-helper-1.0.0.tar.gz" to "/artifactory/<repo>/acme-helper/acme-helper-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "puppet-token"
    When I install Puppet module "acme-widget" from "<repo>" into "modules"
    Then the command succeeds
    And the file "modules/widget/metadata.json" contains "acme-widget"
    And the file "modules/helper/metadata.json" contains "acme-helper"
    Examples:
      | class     | repo         |
      | local     | puppet-local |
      | federated | puppet-fed   |

  Scenario: Puppet follows remote redirects, installs dependencies, then installs offline
    Given the registry is running
    And the Puppet client can reach the registry
    And a Puppet upstream serves the controlled modules
    When I PUT "/artifactory/api/repositories/puppet-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"puppet","url":"{upstream}/puppet/"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "puppet-token"
    When I install Puppet module "acme-widget" from "puppet-remote" into "remote-modules"
    Then the command succeeds
    And the file "remote-modules/helper/metadata.json" contains "acme-helper"
    When I POST "/artifactory/api/repositories/puppet-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    Given the Puppet upstream stops
    When I install Puppet module "acme-widget" from "puppet-remote" into "offline-modules"
    Then the command succeeds
    And the file "offline-modules/helper/metadata.json" contains "acme-helper"

  Scenario: Puppet resolves the root module and dependency across virtual members
    Given the registry is running
    And the Puppet client can reach the registry
    And the Puppet fixtures are ready
    When I PUT "/artifactory/api/repositories/puppet-root" as admin with body:
      """
      {"rclass":"local","packageType":"puppet"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/puppet-deps" as admin with body:
      """
      {"rclass":"local","packageType":"puppet"}
      """
    Then the response status is 200
    When I deploy the file "acme-widget-1.0.0.tar.gz" to "/artifactory/puppet-root/acme-widget/acme-widget-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I deploy the file "acme-helper-1.0.0.tar.gz" to "/artifactory/puppet-deps/acme-helper/acme-helper-1.0.0.tar.gz" as admin
    Then the response status is 201
    When I PUT "/artifactory/api/repositories/puppet-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"puppet","repositories":["puppet-root","puppet-deps"]}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "puppet-token"
    When I install Puppet module "acme-widget" from "puppet-virtual" into "virtual-modules"
    Then the command succeeds
    And the file "virtual-modules/widget/metadata.json" contains "acme-widget"
    And the file "virtual-modules/helper/metadata.json" contains "acme-helper"

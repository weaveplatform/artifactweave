@puppet
Feature: Puppet resolves a module whose releases fill more than one page

  # A module's release list answers 20 releases a page, newest first, under the
  # Forge's pagination object, as 7.x answers it (RTFACT-14557); the Puppet
  # client follows pagination.next to read the rest (#1287).
  Scenario: Puppet installs a release from the second page of a module's release list
    Given the registry is running
    And the Puppet client can reach the registry
    And 25 releases of Puppet module "acme-paged" are ready under "paged"
    When I PUT "/artifactory/api/repositories/puppet-paged" as admin with body:
      """
      {"rclass":"local","packageType":"puppet"}
      """
    Then the response status is 200
    When I deploy the files under "paged" to "/artifactory/puppet-paged/acme-paged" as admin
    When I GET "/artifactory/api/puppet/puppet-paged/v3/releases?module=acme-paged" as admin
    Then the response status is 200
    And the JSON field "results" has 20 entries
    And the JSON field "results.0.version" equals "1.24.0"
    And the JSON field "pagination.next" equals "/v3/releases?module=acme-paged&limit=20&offset=20"
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "puppet-token"
    When I install Puppet module "acme-paged" version "1.0.0" from "puppet-paged" into "paged-modules"
    Then the command succeeds
    And the file "paged-modules/paged/metadata.json" contains "1.0.0"

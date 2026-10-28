@machinelearning
Feature: Native versioned model publication and download
  FrogML 2.1.153 publishes through the transaction API. A fresh SDK process
  resolves the manifest and verifies every downloaded byte without a local cache.

  Scenario Outline: FrogML publishes and downloads from a <class> repository
    Given the registry is running
    And the FrogML client can reach the registry
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"machinelearning"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "frogml-token"
    When I publish the controlled FrogML model to "<repo>"
    Then the command succeeds
    When I download the controlled FrogML model from "<repo>" with a fresh client
    Then the command succeeds
    When I try FrogML download with invalid credentials against "<repo>"
    Then the command fails
    When I try FrogML publish with invalid credentials against "<repo>"
    Then the command fails

    Examples:
      | class     | repo     |
      | local     | ml-local |
      | federated | ml-fed   |

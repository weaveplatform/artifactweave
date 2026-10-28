@nix
Feature: Nix publishes and substitutes signed binary cache content

  Scenario Outline: native Nix publishes and verifies hosted binary cache signatures
    Given the registry is running
    And the Nix client can reach the registry
    And the Nix signing key is installed
    When I PUT "/artifactory/api/repositories/<repo>" as admin with body:
      """
      {"rclass":"<class>","packageType":"nix","primaryKeyPairRef":"nix-signing"}
      """
    Then the response status is 200
    When I publish the controlled Nix path to "<repo>"
    Then the command succeeds
    When I substitute the controlled Nix path from "<repo>" with the "trusted" key
    Then the command succeeds
    And the output contains "native nix fixture"
    When I substitute the controlled Nix path from "<repo>" with the "wrong" key
    Then the command fails
    Examples:
      | class     | repo      |
      | local     | nix-local |
      | federated | nix-fed   |

  Scenario: Nix resolves an ordered virtual member with native signature verification
    Given the registry is running
    And the Nix client can reach the registry
    And the Nix signing key is installed
    When I PUT "/artifactory/api/repositories/nix-member" as admin with body:
      """
      {"rclass":"local","packageType":"nix","primaryKeyPairRef":"nix-signing"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/nix-empty" as admin with body:
      """
      {"rclass":"local","packageType":"nix"}
      """
    Then the response status is 200
    When I publish the controlled Nix path to "nix-member"
    Then the command succeeds
    When I PUT "/artifactory/api/repositories/nix-virtual" as admin with body:
      """
      {"rclass":"virtual","packageType":"nix","repositories":["nix-empty","nix-member"]}
      """
    Then the response status is 200
    When I substitute the controlled Nix path from "nix-virtual" with the "trusted" key
    Then the command succeeds
    And the output contains "native nix fixture"

  Scenario: Nix verifies a remote cache and substitutes with the upstream offline
    Given the registry is running
    And the Nix client can reach the registry
    And the Nix signing key is installed
    And the controlled signed Nix upstream is available
    When I PUT "/artifactory/api/repositories/nix-remote" as admin with body:
      """
      {"rclass":"remote","packageType":"nix","url":"{upstream}","primaryKeyPairRef":"nix-signing"}
      """
    Then the response status is 200
    When I substitute the controlled Nix path from "nix-remote" with the "trusted" key
    Then the command succeeds
    And the output contains "native nix fixture"
    When I POST "/artifactory/api/repositories/nix-remote" as admin with body:
      """
      {"offline":true}
      """
    Then the response status is 200
    And the generic upstream stops
    When I substitute the controlled Nix path from "nix-remote" with the "trusted" key
    Then the command succeeds
    And the output contains "native nix fixture"

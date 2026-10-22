@terraform @terraform_provider_remote
Feature: Remote provider registry and mirror preserve upstream integrity
  The controlled publisher's public key and exact checksum/signature bytes are
  retained. The registry client verifies GPG; the mirror client verifies h1.
  Both paths require a complete upstream-signature-verified package bundle.

  Scenario Outline: native signed provider installs cold and from the offline cache
    Given the registry is running
    And terraform is available
    And the pinned null provider is available
    And a remote Terraform provider upstream serves a signed null provider
    And Terraform installs the remote provider from the "<mode>"
    And the upstream provider signature is "invalid"
    When I run terraform "init -backend=false -no-color"
    Then the command fails
    And the output contains "502"
    And the provider API rejects the invalid upstream signature
    Given the upstream provider signature is "valid"
    When I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the output contains "<verification>"
    And the file ".terraform.lock.hcl" contains "h1:"
    And the upstream provider signer and complete package were used
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "1 added"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/tf-providers" as admin with body:
      """
      {"rclass":"remote","packageType":"terraform","url":"{upstream}","terraformRegistryUrl":"{upstream}","terraformProvidersUrl":"{upstream}","offline":true}
      """
    Then the response status is 200
    When I forget Terraform's provider installation cache
    And I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the file ".terraform.lock.hcl" contains "h1:"
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "No changes"

    Examples:
      | mode     | verification  |
      | registry | self-signed   |
      | mirror   | verified checksum |

  Scenario: native upstream mirror rejects tampering and reuses verified archives offline
    Given the registry is running
    And terraform is available
    And the pinned null provider is available
    And a remote Terraform provider mirror serves content hashes
    And Terraform installs from the upstream provider mirror
    And the upstream mirror archive is "tampered"
    When I run terraform "init -backend=false -no-color"
    Then the command fails
    And the output contains "502"
    Given the upstream mirror archive is "valid"
    When I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the file ".terraform.lock.hcl" contains "h1:"
    And the upstream provider mirror metadata and archive were used
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "1 added"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/tf-providers" as admin with body:
      """
      {"rclass":"remote","packageType":"terraform","url":"{upstream}","terraformRegistryUrl":"{upstream}/smart","terraformProvidersUrl":"{upstream}","offline":true}
      """
    Then the response status is 200
    When I forget Terraform's provider installation cache
    And I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the file ".terraform.lock.hcl" contains "h1:"
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "No changes"

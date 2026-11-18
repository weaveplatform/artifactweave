@terraform @terraform_providers
Feature: Terraform installs signed providers and network mirror archives
  The provider registry verifies the publisher's signature on read. The
  mirror needs only the archive, and serves any valid upstream hostname.

  Background:
    Given the registry is running
    And terraform is available
    And the pinned null provider is available

  Scenario: initialize and use a signed provider from the registry
    Given a null provider is published for the "registry"
    And Terraform installs from the "registry"
    When I run terraform "init -backend=false"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "self-signed"
    And the file ".terraform.lock.hcl" contains "h1:"
    When I run terraform "apply -auto-approve"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "1 added"
    And the server answered 200 to GET "/artifactory/api/terraform/v1/providers/tf-providers-rt-ns-hashicorp/null/versions"

  Scenario: initialize through a TLS mirror without a repository signing key
    Given a null provider is published for the "mirror"
    And Terraform installs from the "mirror"
    When I run terraform "init -backend=false"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file ".terraform.lock.hcl" contains "h1:"
    When I run terraform "apply -auto-approve"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "1 added"
    And the server answered 200 to GET "/artifactory/api/terraform/tf-providers/providers/private.example/hashicorp/null/index.json"

  @terraform_provider_virtual
  Scenario Outline: fresh init installs hosted and private remote providers through a nested virtual <mode>
    Given a virtual Terraform provider repository combines hosted and private remote providers
    And Terraform installs both virtual providers from the "<mode>"
    When I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the file ".terraform.lock.hcl" contains "h1:"
    And the virtual provider bundles preserve both publishers
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "2 added"
    And the server answered no request with 500 since the last command

    Examples:
      | mode     |
      | registry |
      | mirror   |

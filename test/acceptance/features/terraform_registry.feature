@terraform @terraform_registry
Feature: Terraform resolves a private module and provider in one root
  Native version selection, authentication and refresh use the same item
  tree as REST deploy, delete, restore and reindex. The module's selected
  output becomes a provider resource input, so a successful plan exercises
  both private registries together.

  Scenario: newest compatible module, environment credentials and recovery after deletion
    Given the registry is running
    And terraform is available
    And the pinned null provider is available
    And a null provider is published for the "registry"
    When I PUT "/artifactory/api/repositories/tf-modules" as admin with body:
      """
      {"rclass":"local","packageType":"terraform","terraformType":"MODULE"}
      """
    Then the response status is 200
    Given a file "module/main.tf" with content:
      """
      output "release" { value = "1.0.0" }
      """
    And a zip file "1.0.0.zip" of the files under "module"
    And a file "module/main.tf" with content:
      """
      output "release" { value = "1.2.0" }
      """
    And a zip file "1.2.0.zip" of the files under "module"
    And a file "module/main.tf" with content:
      """
      output "release" { value = "2.0.0" }
      """
    And a zip file "2.0.0.zip" of the files under "module"
    When I deploy the file "1.0.0.zip" to "/artifactory/tf-modules/acme/greeting/aws/1.0.0.zip" as admin
    Then the response status is 201
    When I deploy the file "1.2.0.zip" to "/artifactory/tf-modules/acme/greeting/aws/1.2.0.zip" as admin
    Then the response status is 201
    When I deploy the file "2.0.0.zip" to "/artifactory/tf-modules/acme/greeting/aws/2.0.0.zip" as admin
    Then the response status is 201
    Given Terraform uses the discovered registry services
    And a file "main.tf" with expanded content:
      """
      terraform {
        required_providers {
          null = {
            source = "{host}/tf-providers-rt-ns-hashicorp/null"
            version = "3.3.2"
          }
        }
      }
      module "greeting" {
        source = "{host}/tf-modules__acme/greeting/aws"
        version = "~> 1.0"
      }
      resource "null_resource" "proof" {
        triggers = { release = module.greeting.release }
      }
      output "release" { value = module.greeting.release }
      """
    When I run terraform "init -backend=false -no-color"
    Then the command fails
    And the output contains "401"
    And the server answered 401 to GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/versions"
    When I GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/versions"
    Then the response status is 401
    And the JSON field "errors" has 1 entries
    And the response header "WWW-Authenticate" contains "Basic"

    # The environment token must take precedence over the invalid saved token.
    Given a file ".terraform.d/credentials.tfrc.json" with expanded content:
      """
      {"credentials":{"{host}":{"token":"invalid"}}}
      """
    When I run terraform "init -backend=false -no-color" with the kept environment token "provider-token"
    Then the command succeeds
    And Terraform selected module "greeting" at version "1.2.0" in "."
    And the output contains "self-signed"
    And the file ".terraform.lock.hcl" contains "h1:"
    And the file ".terraform/modules/greeting/main.tf" contains "1.2.0"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/1.2.0/download"
    And the server answered 200 to GET "/artifactory/api/terraform/v1/providers/tf-providers-rt-ns-hashicorp/null/versions"
    When I run terraform "plan -no-color -out=registry.tfplan" with the kept environment token "provider-token"
    Then the command succeeds
    And the output contains "1 to add"
    And the output contains "1.2.0"
    When I run terraform "apply -no-color registry.tfplan" with the kept environment token "provider-token"
    Then the command succeeds
    And the output contains "1 added"

    # Files of the wrong Terraform type remain ordinary items; the registry
    # refuses the mismatched route and does not reinterpret their contents.
    When I deploy the file "1.2.0.zip" to "/artifactory/tf-providers/acme/greeting/aws/1.2.0.zip" as admin
    Then the response status is 201
    When I GET "/artifactory/tf-providers/acme/greeting/aws/1.2.0.zip" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/v1/modules/tf-providers__acme/greeting/aws/versions" as admin
    Then the response status is 404
    And the JSON field "errors" has 1 entries
    And the response body contains "MODULE Terraform registry"
    When I GET "/artifactory/api/terraform/v1/providers/tf-modules-rt-ns-hashicorp/null/versions" as admin
    Then the response status is 404
    And the JSON field "errors" has 1 entries
    When I POST "/artifactory/api/terraform/tf-providers/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/v1/modules/tf-providers__acme/greeting/aws/versions" as admin
    Then the response status is 404

    When I DELETE "/artifactory/tf-modules/acme/greeting/aws/1.2.0.zip" as admin
    Then the response status is 204
    When I POST "/artifactory/api/terraform/tf-modules/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/versions" as admin
    Then the response status is 200
    And the JSON field "modules.0.versions" has 2 entries
    And the response body does not contain "1.2.0"
    When I GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/1.2.0/download" as admin
    Then the response status is 404
    When I forget Terraform's module cache
    And I run terraform "init -backend=false -no-color" with the kept environment token "provider-token"
    Then the command succeeds
    And Terraform selected module "greeting" at version "1.0.0" in "."
    And the file ".terraform/modules/greeting/main.tf" contains "1.0.0"
    When I POST "/artifactory/api/trash/restore/tf-modules/acme/greeting/aws/1.2.0.zip?to=tf-modules" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/terraform/tf-modules/reindex" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/versions" as admin
    Then the response status is 200
    And the JSON field "modules.0.versions" has 3 entries
    When I forget Terraform's module cache
    And I run terraform "init -backend=false -no-color" with the kept environment token "provider-token"
    Then the command succeeds
    And Terraform selected module "greeting" at version "1.2.0" in "."
    When I run terraform "plan -no-color -detailed-exitcode" with the kept environment token "provider-token"
    Then the command succeeds
    And the output contains "No changes"

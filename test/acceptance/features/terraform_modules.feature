@terraform @terraform_modules
Feature: Terraform installs modules from the private registry
  The discovery document supplies the module registry URL. Terraform uses
  an access token for registry requests and downloads the signed zip without
  credentials, including when the stored release tag starts with v.

  Scenario: terraform init and apply consume a private module through discovery
    Given the registry is running
    And terraform is available
    When I PUT "/artifactory/api/repositories/tf-modules" as admin with body:
      """
      {"rclass":"local","packageType":"terraform","terraformType":"MODULE"}
      """
    Then the response status is 200
    Given a file "module/main.tf" with content:
      """
      output "greeting" {
        value = "hello from the module registry"
      }
      """
    And a zip file "greeting.zip" of the files under "module"
    When I deploy the file "greeting.zip" to "/artifactory/tf-modules/acme/greeting/aws/v1.0.0.zip" as admin
    Then the response status is 201
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "module-token"
    When I GET "/.well-known/terraform.json"
    Then the response status is 200
    And I keep the JSON field "modules.v1" of the response as "modules-service"
    And I keep the JSON field "providers.v1" of the response as "providers-service"
    # Terraform discovers only over HTTPS. For the binary's HTTP listener,
    # copy its actual discovery values into the supported host override.
    Given a file ".terraformrc" with expanded content:
      """
      host "{host}" {
        services = {
          "modules.v1" = "{modules-service}"
          "providers.v1" = "{providers-service}"
        }
      }
      credentials "{host}" {
        token = "{module-token}"
      }
      """
    And a file "root/main.tf" with expanded content:
      """
      module "greeting" {
        source = "{host}/tf-modules__acme/greeting/aws"
        version = "1.0.0"
      }
      output "greeting" {
        value = module.greeting.greeting
      }
      """
    When I run terraform "-chdir=root init"
    Then the command succeeds
    And the file "root/.terraform/modules/greeting/main.tf" contains "hello from the module registry"
    And the server answered 200 to GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/versions"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-modules__acme/greeting/aws/1.0.0/download"
    When I run terraform "-chdir=root apply -auto-approve"
    Then the command succeeds
    And the output contains "hello from the module registry"
    When I GET "/artifactory/api/terraform/tf-modules/v1/modules/acme/greeting/aws/1.0.0/download" with the kept token "module-token"
    Then the response status is 204
    And the response header "X-Terraform-Get" contains "?sig="
    And I keep the path of the response header "X-Terraform-Get" as "module-download"
    When I GET "{module-download}" as "invalid-user" with password "invalid-password"
    Then the response status is 200

@jf @terraform @terraform_publish
Feature: JFrog publishes Terraform modules and their build-info
  Publishing uses generic item deployment. Terraform then consumes the
  indexed module using an Access token from its credentials file.

  Scenario: publish two modules and a build, then initialize one
    Given the registry is running
    And the JFrog CLI is available
    And terraform is available
    When I PUT "/artifactory/api/repositories/tf-publish" as admin with body:
      """
      {"rclass":"local","packageType":"terraform","terraformType":"MODULE"}
      """
    Then the response status is 200
    Given a file "publish/alpha/main.tf" with content:
      """
      output "name" { value = "alpha" }
      """
    And a file "publish/beta/main.tf" with content:
      """
      output "name" { value = "beta" }
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "terraform-config --server-id-deploy=aw --repo-deploy=tf-publish" in "publish"
    Then the command succeeds
    When I run jf "terraform publish --namespace=acme --provider=aws --tag=v1.2.3 --build-name=terraform-publish --build-number=1" in "publish"
    Then the command succeeds
    When I run jf "rt bp terraform-publish 1"
    Then the command succeeds
    When I GET "/artifactory/api/build/terraform-publish/1" as admin
    Then the response status is 200
    And the JSON field "buildInfo.modules" has 1 entries
    And the JSON field "buildInfo.modules.0.type" equals "terraform"
    And the JSON field "buildInfo.modules.0.artifacts" has 2 entries
    And the JSON field "buildInfo.modules.0.artifacts.0.sha1" is not empty
    And the JSON field "buildInfo.modules.0.artifacts.1.sha1" is not empty
    When I GET "/artifactory/api/storage/tf-publish/acme/alpha/aws/v1.2.3.zip?properties=build.name,build.number" as admin
    Then the response status is 200
    And the response body contains "terraform-publish"
    When I GET "/artifactory/api/storage/tf-publish/acme/beta/aws/v1.2.3.zip" as admin
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "module-token"
    Given a file ".terraformrc" with expanded content:
      """
      host "{host}" {
        services = {
          "modules.v1" = "{url}/api/terraform/v1/modules/"
        }
      }
      """
    And a file ".terraform.d/credentials.tfrc.json" with expanded content:
      """
      {"credentials":{"{host}":{"token":"{module-token}"}}}
      """
    And a file "root/main.tf" with expanded content:
      """
      module "example" {
        source = "{host}/tf-publish__acme/alpha/aws"
        version = "1.2.3"
      }
      output "name" { value = module.example.name }
      """
    When I run terraform "-chdir=root init"
    Then the command succeeds
    And the file "root/.terraform/modules/example/main.tf" contains "alpha"
    When I run terraform "-chdir=root apply -auto-approve"
    Then the command succeeds
    And the output contains "alpha"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-publish__acme/alpha/aws/1.2.3/download"

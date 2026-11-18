@terraform @terraform_remote
Feature: Terraform installs remote modules through authorized archive downloads
  Controlled upstream fixtures prove artifactweave behavior. Vendor parity
  observations remain tracked in P19. Original ZIP and tar archive bytes stay
  separate from raw registry metadata and request-specific signed URLs.

  Scenario: cold pinned archives and nested modules survive upstream outage with a fresh client module cache
    Given the registry is running
    And terraform is available
    And a Terraform registry upstream serves versioned ZIP and subdirectory archives
    When I PUT "/artifactory/api/repositories/tf-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"terraform","url":"{upstream}","terraformRegistryUrl":"{upstream}"}
      """
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "module-token"
    Given Terraform uses the discovered registry services
    And a file ".terraform.d/credentials.tfrc.json" with expanded content:
      """
      {"credentials":{"{host}":{"token":"{module-token}"}}}
      """
    And a file "main.tf" with expanded content:
      """
      module "plain" {
        source = "{host}/tf-proxy__acme/greeting/aws"
        version = "1.0.0"
      }
      module "nested" {
        source = "{host}/tf-proxy__acme/greeting/aws"
        version = "2.0.0"
      }
      output "plain" { value = module.plain.release }
      output "nested" { value = module.nested.release }
      """
    When I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And Terraform selected module "plain" at version "1.0.0" in "."
    And Terraform selected module "nested" at version "2.0.0" in "."
    And the file ".terraform/modules/plain/main.tf" contains "cold ZIP 1.0.0"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-proxy__acme/greeting/aws/2.0.0/download"
    And the Terraform upstream received cold module discovery and archive requests
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "cold ZIP 1.0.0"
    And the output contains "nested tar 2.0.0"
    Given the generic upstream stops
    When I POST "/artifactory/api/repositories/tf-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"terraform","url":"{upstream}","terraformRegistryUrl":"{upstream}","offline":true}
      """
    Then the response status is 200
    When I forget Terraform's module cache
    And I run terraform "init -backend=false -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And Terraform selected module "plain" at version "1.0.0" in "."
    And Terraform selected module "nested" at version "2.0.0" in "."
    And the file ".terraform/modules/plain/main.tf" contains "cold ZIP 1.0.0"
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And the output contains "cold ZIP 1.0.0"
    And the output contains "nested tar 2.0.0"

  @jf @terraform_virtual
  Scenario: a fresh init resolves rewritten HCL and JSON registry dependencies through one virtual
    Given the registry is running
    And terraform is available
    And the JFrog CLI is available
    And a Terraform remote parent depends on local and remote registry submodules
    When I PUT "/artifactory/api/repositories/acme" as admin with body:
      """
      {"rclass":"local","packageType":"terraform","terraformType":"MODULE"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/tf-proxy" as admin with body:
      """
      {"rclass":"remote","packageType":"terraform","url":"{upstream}","terraformRegistryUrl":"{upstream}"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/tf-all" as admin with body:
      """
      {"rclass":"virtual","packageType":"terraform","repositories":["acme","tf-proxy"],"defaultDeploymentRepo":"acme","artifactoryRequestsCanRetrieveRemoteArtifacts":false}
      """
    Then the response status is 200
    Given a file "publish/local/main.tf" with content:
      """
      output "name" { value = "local dependency" }
      """
    When I run jf "config add aw --url {platform} --artifactory-url {url} --user admin --password {password} --interactive=false"
    Then the command succeeds
    When I run jf "terraform-config --server-id-deploy=aw --repo-deploy=tf-all" in "publish"
    Then the command succeeds
    When I run jf "terraform publish --namespace=acme --provider=aws --tag=v1.2.3" in "publish"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    When I GET "/artifactory/api/storage/acme/acme/local/aws/v1.2.3.zip" as admin
    Then the response status is 200
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "module-token"
    Given Terraform uses the discovered registry services
    And a file ".terraform.d/credentials.tfrc.json" with expanded content:
      """
      {"credentials":{"{host}":{"token":"{module-token}"}}}
      """
    And a file "root/main.tf" with expanded content:
      """
      module "parent" {
        source = "{host}/tf-all__acme/greeting/aws"
        version = "2.0.0"
      }
      output "release" { value = module.parent.release }
      """
    When I run terraform "-chdir=root init -backend=false -no-color"
    Then the command succeeds
    And the server answered no request with 500 since the last command
    And Terraform selected module "parent" at version "2.0.0" in "root"
    And Terraform selected module "parent.local" at version "1.2.3" in "root"
    And Terraform selected module "parent.remote" at version "1.0.0" in "root"
    And the file "root/.terraform/modules/parent/snapshot/modules/demo/main.tf" contains "tf-all__acme/local/aws"
    And the file "root/.terraform/modules/parent/snapshot/modules/demo/main.tf.json" contains "tf-all__acme/helper/aws//modules/child"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-all__acme/local/aws/1.2.3/download"
    And the server answered 204 to GET "/artifactory/api/terraform/v1/modules/tf-all__acme/helper/aws/1.0.0/download"
    When I run terraform "-chdir=root apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "local dependency and remote dependency"
    And the server answered no request with 500 since the last command

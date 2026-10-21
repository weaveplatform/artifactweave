@terraform @terraform_backend
Feature: Terraform uses local execution with the remote state backend
  The native client discovers the actual backend services, uploads modern
  state versions, and coordinates locks across independent working copies.

  Background:
    Given the registry is running
    And terraform is available

  Scenario: Apply, advance state and contend for an encrypted backend lock
    Given the pinned null provider is available
    And a null provider is published for the "registry"
    And a Terraform remote backend with name "dev" and encryption enabled
    And the remote configuration uses the private null provider
    When I run terraform "init -no-color"
    Then the command succeeds
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the output contains "1 added"
    When I run terraform "state pull"
    Then the command succeeds
    And Terraform's pulled state matches the latest version and is encrypted through REST
    Given the next apply changes the null resource
    When I run terraform "apply -auto-approve -no-color"
    Then the command succeeds
    And the backend has advanced its state version
    When I run terraform "state pull"
    Then the command succeeds
    And Terraform's pulled state matches the latest version and is encrypted through REST
    And a second Terraform working copy cannot plan while the first holds the lock
    When I POST "/artifactory/api/terraform/remote/v2/workspaces/tf-state__dev/actions/lock" as admin
    Then the response status is 200
    When I run terraform "force-unlock -force tf-state/dev"
    Then the command succeeds
    When I run terraform "plan -lock-timeout=0s -no-color"
    Then the command succeeds

  Scenario: Prefix workspaces can be created, listed and deleted
    Given a Terraform remote backend with prefix "app-" and encryption enabled
    When I run terraform "init -no-color"
    Then the command fails
    And the output contains "No existing workspaces"
    When I run terraform "workspace new dev"
    Then the command succeeds
    When I run terraform "init -no-color"
    Then the command succeeds
    When I run terraform "workspace new test"
    Then the command succeeds
    When I run terraform "workspace list"
    Then the command succeeds
    And the output contains "dev"
    And the output contains "test"
    When I run terraform "workspace select dev"
    Then the command succeeds
    When I run terraform "workspace delete test"
    Then the command succeeds
    When I GET "/artifactory/api/terraform/remote/v2/organizations/tf-state/workspaces/app-test" as admin
    Then the response status is 404

  Scenario: Large plain state survives upload and a deleted workspace can be recreated
    Given a Terraform remote backend with name "dev" and encryption disabled
    And a Terraform state of 21 MiB is ready to push
    When I run terraform "init -no-color"
    Then the command succeeds
    When I run terraform "state push large.tfstate"
    Then the command succeeds
    When I run terraform "state pull"
    Then the command succeeds
    And Terraform's pulled state matches the latest version and is plain through REST
    When I DELETE "/artifactory/tf-state/workspaces/dev" as admin
    Then the response status is 204
    When I run terraform "init -reconfigure -no-color"
    Then the command succeeds
    When I GET "/artifactory/api/terraform/remote/v2/organizations/tf-state/workspaces/dev" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/remote/v2/workspaces/tf-state__dev/current-state-version" as admin
    Then the response status is 404

  Scenario: Login checks and stores a pasted token and expired bearers are unauthorized
    Given a Terraform remote backend with name "dev" and encryption enabled
    When Terraform logs in with the issued token
    And I run terraform "init -no-color"
    Then the command succeeds
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"expires_in":1}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "expired-backend-token"
    And the access token kept as "expired-backend-token" has expired
    When I GET "/artifactory/api/terraform/remote/v2/account/details" with the kept token "expired-backend-token"
    Then the response status is 401
    And the JSON field "errors.0.status" equals "401"
    Given Terraform uses the kept backend token "expired-backend-token"
    When I run terraform "state pull"
    Then the command fails
    And the output contains "unauthorized"
    And the server answered 401 to GET "/artifactory/api/terraform/remote/v2/organizations/tf-state/entitlement-set"

@terraform @terraform_backend_api
Feature: The remote backend discovers and stores encrypted workspace state
  The backend API advertises local execution, preserves older inline state
  uploads, and permits the interim terraform login token check.

  Scenario: Backend discovery, login, workspace and inline state round trip
    Given the registry is running
    When I PUT "/artifactory/api/repositories/tf-state" as admin with body:
      """
      {"rclass":"local","packageType":"terraformbackend"}
      """
    Then the response status is 200
    When I GET "/.well-known/terraform.json"
    Then the response status is 200
    And I keep the JSON field "state.v2" of the response as "state.v2-service"
    And I keep the JSON field "tfe.v2" of the response as "tfe.v2-service"
    And I keep the JSON field "tfe.v2.1" of the response as "tfe.v2.1-service"
    And I keep the JSON field "tfe.v2.2" of the response as "tfe.v2.2-service"
    When I GET "/artifactory/api/terraform/remote/v2/ping"
    Then the response status is 204
    When I GET "/artifactory/api/terraform/remote/v2/account/details" as admin
    Then the response status is 200
    And the JSON field "data.type" equals "users"
    And the JSON field "data.attributes.username" equals "admin"
    When I POST "/artifactory/api/terraform/remote/v2/organizations/tf-state/workspaces" as admin with body:
      """
      {"data":{"type":"workspaces","attributes":{"name":"app","terraform-version":"1.2.9"}}}
      """
    Then the response status is 201
    And the JSON field "data.id" equals "tf-state__app"
    And the JSON field "data.attributes.execution-mode" equals "local"
    When I POST "/artifactory/api/terraform/remote/v2/workspaces/tf-state__app/actions/lock" as admin
    Then the response status is 200
    When I POST "/artifactory/api/terraform/remote/v2/workspaces/tf-state__app/state-versions" as admin with body:
      """
      {"data":{"type":"state-versions","attributes":{"serial":1,"md5":"ac99888cabac83a0f0623dfd585661d6","state":"eyJ2ZXJzaW9uIjo0LCJzZXJpYWwiOjEsImxpbmVhZ2UiOiJhY2NlcHRhbmNlIiwib3V0cHV0cyI6e30sInJlc291cmNlcyI6W119"}}}
      """
    Then the response status is 201
    And the JSON field "data.attributes.status" equals "finalized"
    And I keep the JSON field "id" of the response as "state-version"
    When I GET "/artifactory/api/terraform/remote/v2/state-versions/{state-version}/download" as admin
    Then the response status is 200
    And the JSON field "serial" is the number 1
    When I POST "/artifactory/api/terraform/remote/v2/workspaces/tf-state__app/actions/unlock" as admin
    Then the response status is 200
    When I GET "/artifactory/api/terraform/remote/v2/account/details"
    Then the response status is 401
    And the JSON field "errors.0.status" equals "401"
    When I DELETE "/artifactory/api/terraform/remote/v2/organizations/tf-state/workspaces/app" as admin
    Then the response status is 204

Feature: Authentication
  Basic credentials against the seeded admin (decision 0005). Probes and
  version reads answer without credentials; everything else is challenged
  until anonymous access is turned on.

  Background:
    Given the registry is running

  Scenario: A call without credentials is challenged
    When I GET "/artifactory/api/repositories"
    Then the response status is 401
    And the response header "WWW-Authenticate" contains "Artifactory Realm"
    And the JSON field "errors.0.message" equals "Authentication is required"

  Scenario: Wrong credentials are refused even on a probe
    When I GET "/artifactory/api/system/ping" as "admin" with password "wrong"
    Then the response status is 401
    And the JSON field "errors.0.message" equals "Bad credentials"

  Scenario: The seeded admin is accepted
    When I GET "/artifactory/api/repositories" as admin
    Then the response status is not 401

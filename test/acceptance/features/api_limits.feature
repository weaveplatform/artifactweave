@api_reliability
Feature: Incoming API request budgets
  Rate metadata describes the caller's allowance and keeps health checks available.

  Scenario: An exhausted principal budget returns retry metadata
    Given the registry is running with environment "ARTIFACTWEAVE_API_PRINCIPAL_PER_MINUTE=1"
    When I GET "/artifactory/api/system/version" as admin
    Then the response status is 200
    And the response header "RateLimit-Policy" contains "q=1"
    When I GET "/artifactory/api/system/version" as admin
    Then the response status is 429
    And the response header "RateLimit" contains "r=0"
    When I GET "/router/api/v1/system/liveness"
    Then the response status is 200

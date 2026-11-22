@api_reliability @storage @loggers
Feature: Bounded temporary logger administration
  Logger names are artifactweave component names. Runtime emission, redaction,
  expiry, replacement races and shutdown are also checked by the Go suites.

  Background:
    Given the registry is running

  Scenario: A temporary level is visible until an administrator deletes it
    When I DELETE "/artifactory/api/system/debug/loggers" as admin
    Then the response status is 200
    When I POST "/artifactory/api/system/debug/loggers" as admin with body:
      """
      {"classes":{"artifactweave.http":"debug"},"timer":60}
      """
    Then the response status is 200
    When I GET "/artifactory/api/system/debug/loggers/artifactweave.http" as admin
    Then the response status is 200
    And the response body contains "debug"
    When I POST "/artifactory/api/system/debug/loggers" as admin with body:
      """
      {"classes":{"artifactweave.http":"error"},"timer":60}
      """
    Then the response status is 400
    When I DELETE "/artifactory/api/system/debug/loggers" as admin
    Then the response status is 200
    When I GET "/artifactory/api/system/debug/loggers/artifactweave.http" as admin
    Then the response status is 200
    And the response body contains "info"

  Scenario: An invalid session leaves the baseline level intact
    When I POST "/artifactory/api/system/debug/loggers" as admin with body:
      """
      {"classes":{"artifactweave.http":"debug"},"timer":0}
      """
    Then the response status is 400
    When I GET "/artifactory/api/system/debug/loggers/artifactweave.http" as admin
    Then the response status is 200
    And the response body contains "info"

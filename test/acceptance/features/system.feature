Feature: System endpoints
  The probes and version endpoints that every JFrog client calls before doing
  anything else. The exact bodies and status codes are what the JFrog CLI and
  the Terraform providers parse (research 06 and 09). The Artifactory version
  needs credentials while anonymous access is off; the clients send theirs.

  Background:
    Given the registry is running

  Scenario: Artifactory ping answers OK
    When I GET "/artifactory/api/system/ping"
    Then the response status is 200
    And the response body is "OK"

  Scenario: Artifactory version reports the compatibility version and a revision
    When I GET "/artifactory/api/system/version" as admin
    Then the response status is 200
    And the JSON field "version" equals "7.155.0"
    And the JSON field "revision" is not empty
    And the JSON field "addons" exists

  Scenario: Artifactory version is refused without credentials while ping is not
    When I GET "/artifactory/api/system/version"
    Then the response status is 401
    And the response header "WWW-Authenticate" contains "Artifactory Realm"
    And the JSON field "errors.0.message" equals "Authentication is required"
    When I GET "/artifactory/api/system/ping"
    Then the response status is 200
    And the response body is "OK"

  Scenario: Readiness and liveness
    When I GET "/artifactory/api/v1/system/readiness"
    Then the response status is 200
    And the JSON field "code" equals "OK"
    When I GET "/artifactory/api/v1/system/liveness"
    Then the response status is 200
    And the JSON field "code" equals "OK"

  Scenario: Access ping and version
    When I GET "/access/api/v1/system/ping"
    Then the response status is 200
    And the response body is "OK"
    When I GET "/access/api/v1/system/version"
    Then the response status is 200
    And the JSON field "name" equals "7.155.0"
    And the JSON field "version" equals "7.155.0"

  Scenario: Router health
    When I GET "/router/api/v1/system/health"
    Then the response status is 200
    And the JSON field "router.state" equals "HEALTHY"

  Scenario: Usage reports are accepted and ignored
    When I POST "/artifactory/api/system/usage" with body:
      """
      {"productId":"jfrog-cli-go/2.126.0","features":[]}
      """
    Then the response status is 200
    When I POST "/jfconnect/api/v1/backoffice/metrics/log" with body:
      """
      {"metrics":[]}
      """
    Then the response status is 200

  # The sinks answer without credentials, so their bodies are capped at
  # 1 MiB (#431).
  Scenario: A telemetry body over 1 MiB is refused
    When I POST "/artifactory/api/system/usage" with a body of 1048577 bytes
    Then the response status is 413
    And the JSON field "errors.0.status" equals "413"
    When I POST "/jfconnect/api/v1/backoffice/metrics/log" with a body of 1048577 bytes
    Then the response status is 413

  Scenario: Xray is stubbed in its own envelope
    When I GET "/xray/api/v1/system/version" as admin
    Then the response status is 501
    And the JSON field "error" is not empty

  Scenario: A missing repository uses the Artifactory error envelope
    When I GET "/artifactory/api/nonexistent/missing" as admin
    Then the response status is 404
    And the JSON field "errors.0.status" equals "404"
    And the JSON field "errors.0.message" is not empty

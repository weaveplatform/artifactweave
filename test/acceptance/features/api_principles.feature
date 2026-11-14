@api_reliability @api_principles
Feature: Idempotency keys, rate limits and paging through the built server
  Decision 0043's three principles together, as a client of the binary meets
  them: a key that makes a lost answer safe to retry, the metadata of a spent
  allowance, and a list read to its end by its next links. Each is an
  artifactweave extension, not captured JFrog behavior.

  Scenario: A lost answer is retried with its key, an allowance is spent and a list is walked
    # The limit is low enough to spend in a few dozen requests, and high
    # enough that the administrator's own requests below never reach it.
    Given the registry is running with environment "ARTIFACTWEAVE_API_PRINCIPAL_PER_MINUTE=60"
    # Three runs of one number: a delete of the number removes the earliest,
    # so an unkeyed repeat would remove a second run.
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name":"retry-app","number":"1","started":"2026-10-15T09:30:00.000+0000","modules":[]}
      """
    Then the response status is 204
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name":"retry-app","number":"1","started":"2026-10-15T09:31:00.000+0000","modules":[]}
      """
    Then the response status is 204
    When I PUT "/artifactory/api/build" as admin with body:
      """
      {"name":"retry-app","number":"1","started":"2026-10-15T09:32:00.000+0000","modules":[]}
      """
    Then the response status is 204
    Given the next request has idempotency key "con03-delete"
    And the transport drops the next response
    When I POST "/artifactory/api/build/delete" as admin with body:
      """
      {"buildName":"retry-app","buildNumbers":["1"]}
      """
    Then the client saw no response
    Given the next request has idempotency key "con03-delete"
    When I POST "/artifactory/api/build/delete" as admin with body:
      """
      {"buildName":"retry-app","buildNumbers":["1"]}
      """
    Then the response status is 200
    And the response is the one the transport dropped
    When I GET "/artifactory/api/build/retry-app" as admin
    Then the JSON field "buildsNumbers" has 2 entries
    Given the next request has idempotency key "con03-delete"
    When I POST "/artifactory/api/build/delete" as admin with body:
      """
      {"buildName":"retry-app","buildNumbers":["1","1"]}
      """
    Then the response status is 422
    When I GET "/artifactory/api/build/retry-app" as admin
    Then the JSON field "buildsNumbers" has 2 entries
    # A reader of three repositories out of five. The seeded Anything
    # permission would let every user read every repository.
    When I PUT "/artifactory/api/repositories/walk-a" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/walk-b" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/walk-c" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/walk-d" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/api/repositories/walk-e" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """
    Then the response status is 200
    When I DELETE "/artifactory/api/security/permissions/Anything" as admin
    Then the response status is 200
    When I PUT "/artifactory/api/security/users/walker" as admin with body:
      """
      {"name": "walker", "email": "walker@example.com", "password": "Walker-pass1"}
      """
    Then the response status is 201
    When I PUT "/artifactory/api/security/permissions/walk-read" as admin with body:
      """
      {"name": "walk-read", "repositories": ["walk-a", "walk-c", "walk-e"],
       "principals": {"users": {"walker": ["r"]}}}
      """
    Then the response status is 200
    When I walk "/artifactory/api/repositories?limit=2" as "walker" with password "Walker-pass1" by its next links
    Then the walk read 2 pages
    And the walk visited each "key" of "/artifactory/api/repositories" exactly once
    And the walk visited the "key" values "walk-a,walk-c,walk-e"
    # The same principal, driven past its allowance.
    When I GET "/artifactory/api/system/version" as "walker" with password "Walker-pass1" until its allowance is spent
    Then the response status is 429
    And the response header "Retry-After" is a number from 1 to 60
    And the response header "RateLimit" contains "r=0"
    And the response header "RateLimit-Policy" contains "q=60;w=60"

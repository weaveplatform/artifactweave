@api_reliability @federation_monitoring
Feature: Federation monitoring and RTFS resource boundaries
  Operators read durable repository state through the v1/v2 status APIs.
  Multi-member partition, recovery and receiver observations also run in
  test/peers on SQLite and PostgreSQL.

  Background:
    Given the registry is running

  Scenario: An empty configured instance reports all eight numeric state counters
    When I GET "/artifactory/api/federation/status/stateList" as admin
    Then the response status is 200
    And the JSON field "federations" has 0 entries
    When I GET "/artifactory/api/federation/status/stateSummary" as admin
    Then the response status is 200
    And the JSON field "federationStates.HEALTHY" is the number 0
    And the JSON field "federationStates.READ_ONLY" is the number 0
    And the JSON field "federationStates.NOT_AVAILABLE" is the number 0
    And the JSON field "federationStates.DELAYED" is the number 0
    And the JSON field "federationStates.PENDING_FS" is the number 0
    And the JSON field "federationStates.FULL_SYNC_RUNNING" is the number 0
    And the JSON field "federationStates.ERROR" is the number 0
    And the JSON field "federationStates.DISABLED" is the number 0
    When I GET "/artifactory/api/federation/status/stateList?status=READ_ONLY" as admin
    Then the response status is 200
    And the JSON field "federations" has 0 entries
    When I GET "/artifactory/api/federation/rtfs" as admin
    Then the response status is 200
    And the response body is "true"
    When I GET "/artifactory/api/federation/migration/status" as admin
    Then the response status is 200
    And the response body is "MIGRATION_COMPLETED"

  Scenario: Repository status and projections report actual hosted content
    When I PUT "/artifactory/api/repositories/observed-fed" as admin with body:
      """
      {"rclass":"federated","packageType":"generic"}
      """
    Then the response status is 200
    When I PUT "/artifactory/observed-fed/hello.txt" as admin with body:
      """
      hello
      """
    Then the response status is 201
    When I GET "/artifactory/api/federation/status/repo/observed-fed" as admin
    Then the response status is 200
    And the JSON field "localKey" equals "observed-fed"
    And the JSON field "mirrorEventsStatusInfo" has 0 entries
    And the JSON field "binariesTasksInfo.failingTasks" is the number 0
    And the JSON field "binariesTasksInfo.inProgressTasks" is the number 0
    And the JSON field "federatedArtifactStatus.countFullyReplicateArtifacts" is the number 1
    And the JSON field "federatedArtifactStatus.countArtificiallyReplicatedArtifacts" is the number 0
    When I GET "/artifactory/api/federation/status/repo/observed-fed/mirrors" as admin
    Then the response status is 200
    And the JSON field "mirrorEventsStatusInfo" has 0 entries
    And the JSON field "binariesTasksInfo" does not exist
    When I GET "/artifactory/api/v2/federation/status/repo/observed-fed/binaries" as admin
    Then the response status is 200
    And the JSON field "mirrorEventsStatusInfo" does not exist
    And the JSON field "federatedArtifactStatus.countFullyReplicateArtifacts" is the number 1
    When I GET "/artifactory/api/federation/status/repo/observed-fed/invalid" as admin
    Then the response status is 400
    When I GET "/artifactory/api/federation/status/repo/missing/mirrors" as admin
    Then the response status is 404

  Scenario Outline: Monitoring requires a current administrator
    When I GET "<path>"
    Then the response status is 401
    When I POST "/access/api/v2/users" as admin with body:
      """
      {"username":"observer","email":"observer@example.com","password":"Observer-pass1"}
      """
    Then the response status is 201
    When I GET "<path>" as "observer" with password "Observer-pass1"
    Then the response status is 403
    When I GET "<path>" as admin
    Then the response status is <adminStatus>

    Examples:
      | path                                                 | adminStatus |
      | /artifactory/api/federation/status/stateList           | 200         |
      | /artifactory/api/federation/status/stateSummary        | 200         |
      | /artifactory/api/federation/inconsistentRepos          | 200         |
      | /artifactory/api/federation/status/repo/missing        | 404         |

  Scenario: Migration and internal resources keep their fixed refusal answers
    When I PUT "/artifactory/api/federation/migration" as admin
    Then the response status is 400
    And the response body contains "Access denied: This endpoint is restricted to migration tool calls only."
    When I GET "/artifactory/api/federation/status"
    Then the response status is 404
    When I GET "/artifactory/api/federation/status" as admin
    Then the response status is 404

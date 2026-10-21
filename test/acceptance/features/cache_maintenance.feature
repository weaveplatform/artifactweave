@cachemaintenance
Feature: Remote cache maintenance
  Metadata expiry keeps usable bytes. Explicit cache deletion and unused cleanup
  remove retrieval state without sending cache-alias requests upstream.

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/maintenance" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"http://127.0.0.1:9","unusedArtifactsCleanupPeriodHours":1}
      """
    Then the response status is 200

  Scenario: Metadata zap keeps binary content and deletion clears a confirmed miss
    Given the remote "maintenance" has aged "unique" content at "pkg/artifact.bin" containing "immutable bytes"
    And the remote "maintenance" has aged "mutable" content at "pkg/snapshot.bin" containing "mutable bytes"
    And the remote "maintenance" has aged "metadata" content at "pkg/index" containing "all upstream versions"
    And the remote "maintenance" has a negative entry at "pkg/new.bin"
    When I POST "/artifactory/api/zap/maintenance-cache/pkg" as admin with body:
      """
      """
    Then the response status is 200
    And the remote "maintenance" retains expired metadata at "pkg/index" containing "all upstream versions"
    When I GET "/artifactory/maintenance-cache/pkg/artifact.bin" as admin
    Then the response status is 200
    And the response body is "immutable bytes"
    When I GET "/artifactory/maintenance-cache/pkg/snapshot.bin" as admin
    Then the response status is 200
    And the response body is "mutable bytes"
    When I DELETE "/artifactory/maintenance-cache/pkg/new.bin" as admin
    Then the response status is 204
    When I DELETE "/artifactory/maintenance-cache/pkg/artifact.bin" as admin
    Then the response status is 204
    When I GET "/artifactory/maintenance-cache/pkg/artifact.bin" as admin
    Then the response status is 404
    When I GET "/artifactory/api/storage/auto-trashcan/maintenance/pkg/artifact.bin" as admin
    Then the response status is 404

  Scenario: Cleanup removes unused bytes while a successful cache download protects content
    Given the remote "maintenance" has aged "unique" content at "unused.bin" containing "unused"
    And the remote "maintenance" has aged "unique" content at "used.bin" containing "recently used"
    When I GET "/artifactory/maintenance-cache/used.bin" as admin
    Then the response status is 200
    When I run remote cache cleanup
    And I GET "/artifactory/maintenance-cache/unused.bin" as admin
    Then the response status is 404
    When I GET "/artifactory/maintenance-cache/used.bin" as admin
    Then the response status is 200
    And the response body is "recently used"

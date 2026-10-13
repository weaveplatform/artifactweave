Feature: Trash can and garbage collection
  Deleted items wait in the trash can until restored or purged, and garbage
  collection reclaims the content nothing references any more (decision 0010).

  Background:
    Given the registry is running
    And I PUT "/artifactory/api/repositories/trash-local" as admin with body:
      """
      {"rclass": "local", "packageType": "generic"}
      """

  Scenario: Restore a deleted file from the trash can
    When I PUT "/artifactory/trash-local/docs/a.txt" as admin with body:
      """
      restore me
      """
    And I DELETE "/artifactory/trash-local/docs/a.txt" as admin
    Then the response status is 204
    When I GET "/artifactory/trash-local/docs/a.txt" as admin
    Then the response status is 404
    When I GET "/artifactory/api/storage/auto-trashcan/trash-local/docs/a.txt" as admin
    Then the response status is 200
    And the JSON field "size" equals "10"
    When I POST "/artifactory/api/trash/restore/trash-local/docs?to=trash-local" as admin with body:
      """
      {}
      """
    Then the response status is 202
    And the response body is "Successfully restored trash items"
    When I GET "/artifactory/trash-local/docs/a.txt" as admin
    Then the response status is 200
    And the response body is "restore me"

  Scenario: Emptying the trash can and collecting garbage frees the content
    When I PUT "/artifactory/trash-local/collect/me.txt" as admin with body:
      """
      collect me
      """
    And I GET "/artifactory/api/storageinfo" as admin
    Then the JSON field "binariesSummary.binariesCount" equals "1"
    When I DELETE "/artifactory/trash-local/collect/me.txt" as admin
    And I POST "/artifactory/api/system/storage/gc" as admin with body:
      """
      {}
      """
    Then the response status is 200
    When I GET "/artifactory/api/storageinfo" as admin
    Then the JSON field "binariesSummary.binariesCount" equals "1"
    When I POST "/artifactory/api/trash/empty" as admin with body:
      """
      {}
      """
    Then the response status is 202
    When I POST "/artifactory/api/system/storage/gc" as admin with body:
      """
      {}
      """
    Then the response status is 200
    When I GET "/artifactory/api/storageinfo" as admin
    Then the JSON field "binariesSummary.binariesCount" equals "0"

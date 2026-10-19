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

  Scenario: The trash can answers the storage API's views
    When I PUT "/artifactory/trash-local/docs/a.txt" as admin with body:
      """
      list me
      """
    And I DELETE "/artifactory/trash-local/docs" as admin
    Then the response status is 204
    When I GET "/artifactory/api/storage/auto-trashcan/trash-local?list&deep=1&listFolders=1" as admin
    Then the response status is 200
    And the JSON field "files" has 2 entries
    And the JSON field "files.0.uri" equals "/docs"
    And the JSON field "files.1.uri" equals "/docs/a.txt"
    And the JSON field "files.1.size" is the number 7
    When I GET "/artifactory/api/storage/auto-trashcan/trash-local/docs/a.txt?properties" as admin
    Then the response status is 200
    And the response body contains "trash.originalRepositoryType"
    And the response body contains "generic"
    When I GET "/artifactory/api/storage/auto-trashcan/trash-local/docs/a.txt?stats" as admin
    Then the response status is 200
    And the JSON field "downloadCount" is the number 0
    When I GET "/artifactory/api/storage/auto-trashcan/trash-local/docs?lastModified" as admin
    Then the response status is 200
    And the JSON field "lastModified" is not empty

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

  # The fields jf transfer-files and jfrog-client-go read (#390). A run of
  # transfer-files itself needs a source Artifactory with JFrog's
  # data-transfer plugin; internal/server/storageinfo_test.go decodes the
  # summary with the clients' own structs and parsers instead.
  Scenario: The storage summary counts the trash can, and a token scoped to it reads it
    When I PUT "/artifactory/trash-local/docs/a.txt" as admin with body:
      """
      count me
      """
    And I DELETE "/artifactory/trash-local/docs/a.txt" as admin
    And I POST "/access/api/v2/users" as admin with body:
      """
      {"username": "watcher", "email": "watcher@example.com", "password": "Watcher-pass1"}
      """
    Then the response status is 201
    When I GET "/artifactory/api/storageinfo" as "watcher" with password "Watcher-pass1"
    Then the response status is 403
    When I POST "/access/api/v1/tokens" as admin with body:
      """
      {"username": "watcher", "scope": "system:info/storage:r", "expires_in": 3600}
      """
    Then the response status is 200
    And I keep the JSON field "access_token" of the response as "storage-reader"
    When I GET "/artifactory/api/storageinfo" with the kept token "storage-reader"
    Then the response status is 200
    And the JSON field "binariesSummary.artifactsCount" equals "1"
    And the JSON field "binariesSummary.artifactsSize" equals "8 bytes"
    And the JSON field "binariesSummary.optimization" equals "100%"
    And the JSON field "fileStoreSummary.storageType" equals "file-system"
    And the JSON field "fileStoreSummary.storageDirectory" is not empty
    And the JSON field "fileStoreSummary.totalSpace" is not empty
    And the JSON field "repositoriesSummaryList.1.repoKey" equals "auto-trashcan"
    And the JSON field "repositoriesSummaryList.1.repoType" equals "NA"
    And the JSON field "repositoriesSummaryList.1.packageType" equals "NA"
    And the JSON field "repositoriesSummaryList.1.projectKey" equals "default"
    And the JSON field "repositoriesSummaryList.1.filesCount" equals "1"
    And the JSON field "repositoriesSummaryList.1.foldersCount" equals "2"
    And the JSON field "repositoriesSummaryList.1.usedSpaceInBytes" equals "8"
    And the JSON field "repositoriesSummaryList.1.percentage" equals "100%"
    And the JSON field "repositoriesSummaryList.2.repoKey" equals "trash-local"
    And the JSON field "repositoriesSummaryList.2.packageType" equals "Generic"
    And the JSON field "repositoriesSummaryList.2.usedSpaceInBytes" equals "0"
    And the JSON field "repositoriesSummaryList.2.percentage" equals "0%"
    And the JSON field "repositoriesSummaryList.3.repoKey" equals "TOTAL"
    And the JSON field "repositoriesSummaryList.3.usedSpace" equals "8 bytes"
    And the JSON field "repositoriesSummaryList.3.usedSpaceInBytes" equals "8"
    Given the next request has header "Authorization" "Bearer {storage-reader}"
    When I POST "/artifactory/api/storageinfo/calculate" with body:
      """
      {}
      """
    Then the response status is 403

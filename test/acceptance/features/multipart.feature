@jf
Feature: JFrog CLI multipart upload
  The JFrog CLI uploads a file of --min-split MiB or more in parts when
  /api/v1/uploads/config says it may: create, a signed URL per part, the parts
  PUT concurrently, complete, a status poll, then a checksum deploy that
  stores the item (internal/service/multipart).

  Scenario: a large file is uploaded in parts and downloads unchanged
    Given the registry is running
    And the JFrog CLI is available
    And I PUT "/artifactory/api/repositories/libs-local" as admin with body:
      """
      {"rclass":"local","packageType":"generic"}
      """
    And a file "large.bin" of 3670021 bytes
    When I run jf "rt u large.bin libs-local/large/ --flat --min-split=1 --chunk-size=1 --split-count=3 --url {url} --user admin --password {password}"
    Then the command succeeds
    And the server answered 200 to POST "/artifactory/api/v1/uploads/create"
    And the server answered 200 to PUT "/artifactory/api/v1/uploads/part"
    And the server answered 202 to POST "/artifactory/api/v1/uploads/complete"
    And the server answered 201 to PUT "/artifactory/libs-local/large/large.bin"
    When I run jf "rt dl libs-local/large/large.bin downloaded/ --flat --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "large.bin" and "downloaded/large.bin" are identical

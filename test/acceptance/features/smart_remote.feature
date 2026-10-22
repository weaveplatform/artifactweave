@jf @smart_remote
Feature: Smart Remote synchronization uses bounded authenticated peer operations
  The peer is controlled test data, not an observed Artifactory wire capture.

  Background:
    Given the registry is running
    And the JFrog CLI is available
    And a controlled Smart Remote peer serves an artifact
    And a file "workspace.marker" with content:
      """
      Smart Remote native fixture
      """

  Scenario: Native downloads copy and refresh properties, propagate counts and retain absent origin bytes
    When I PUT "/artifactory/api/repositories/smart" as admin with body:
      """
      {"rclass":"remote","packageType":"maven","url":"{smart_upstream}","username":"peer","password":"peer-secret","remoteRepoChecksumPolicyType":"ignore-and-generate","retrievalCachePeriodSecs":0,"contentSynchronisation":{"enabled":true,"properties":{"enabled":true},"statistics":{"enabled":true},"source":{"originAbsenceDetection":true}}}
      """
    Then the response status is 200
    When I run jf "rt direct-download smart/file.bin first/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the Smart peer eventually records 1 downloads
    When I GET "/artifactory/api/storage/smart-cache/file.bin?properties" as admin
    Then the response status is 200
    And the JSON field "properties.color.0" equals "blue"
    Given the Smart peer property becomes "red"
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/smart/file.bin --output second.bin"
    Then the command succeeds
    And the files "first/file.bin" and "second.bin" are identical
    And the Smart peer eventually records 2 downloads
    When I GET "/artifactory/api/storage/smart-cache/file.bin?properties" as admin
    Then the response status is 200
    And the JSON field "properties.color.0" equals "red"
    Given the Smart peer removes its origin artifact
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/smart/file.bin --output absent.bin"
    Then the command succeeds
    And the files "second.bin" and "absent.bin" are identical
    And the Smart peer eventually records 3 downloads
    When I GET "/artifactory/api/artifactweave/remote-status/smart?path=file.bin" as admin
    Then the response status is 200
    And the JSON field "representations.0.origin_absent" equals "true"
    Given the Smart peer becomes unavailable
    When I run jf "rt direct-download smart-cache/file.bin offline/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the files "first/file.bin" and "offline/file.bin" are identical
    And the Smart peer eventually records 3 downloads

  Scenario: Legacy initial synchronization does not refresh properties or propagate statistics
    When I PUT "/artifactory/api/repositories/initial" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{smart_upstream}","username":"peer","password":"peer-secret","synchronizeProperties":true}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/initial/file.bin --output initial.bin"
    Then the command succeeds
    Given the Smart peer property becomes "red"
    When I run jf "rt direct-download initial/file.bin warm/ --flat --fail-no-op --url {url} --user admin --password {password}"
    Then the command succeeds
    And the Smart peer received 1 property requests
    And the Smart peer eventually records 0 downloads
    When I GET "/artifactory/api/storage/initial-cache/file.bin?properties" as admin
    Then the response status is 200
    And the JSON field "properties.color.0" equals "blue"

  Scenario: Disabled synchronization flags do not copy properties or report downloads
    When I PUT "/artifactory/api/repositories/plain" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{smart_upstream}","username":"peer","password":"peer-secret","contentSynchronisation":{"enabled":true,"properties":{"enabled":false},"statistics":{"enabled":false},"source":{"originAbsenceDetection":false}}}
      """
    Then the response status is 200
    When I run curl "--fail --silent --show-error --user admin:{password} {url}/plain/file.bin --output plain.bin"
    Then the command succeeds
    And the Smart peer received 0 property requests
    And the Smart peer eventually records 0 downloads
    When I GET "/artifactory/api/artifactweave/remote-status/plain?path=file.bin" as admin
    Then the response status is 200
    And the JSON field "representations" has 0 entries

  Scenario: A Smart source pointing back to itself stops at the loop fence
    When I PUT "/artifactory/api/repositories/loop" as admin with body:
      """
      {"rclass":"remote","packageType":"generic","url":"{url}/loop","username":"admin","password":"{password}","contentSynchronisation":{"enabled":true}}
      """
    Then the response status is 200
    When I run curl "--max-time 10 --silent --show-error --user admin:{password} {url}/loop/file.bin --output loop.json --write-out %{http_code}"
    Then the command succeeds
    And the output contains "502"
    When I GET "/artifactory/loop-cache/file.bin" as admin
    Then the response status is 404
